// Package docstore keeps one parsed JSON document for each path of a metadata
// storage. A pod can drop its documents at any time, because the storage
// holds the truth.
//
// The rules of the store:
//
//   - An entry is read-only. The store never changes an entry after it
//     publishes it. It only replaces the entry with a new one.
//   - A reader never changes a document that it gets from the store.
//   - A read makes a conditional download. A 304 answer keeps the entry.
//   - A write changes a private copy and uploads it with an etag condition
//     (compare and swap). The store replaces the entry only after a
//     successful upload.
//   - After a conflict, the write downloads the document again and runs the
//     change again. There is no merge.
package docstore

import (
	"context"
	"fmt"
	"path"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/cas"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

const (
	tracerName = "docstore"

	// maxReadAttempts is the number of tries of one download with a
	// transient error.
	maxReadAttempts = 20
	// maxWriteAttempts is the number of tries of one Update. It is the same
	// limit for all kinds of documents.
	maxWriteAttempts = 100
	// defaultReadTimeout is the time limit of one shared download. A caller
	// that waits for the download also has its own context.
	defaultReadTimeout = 60 * time.Second
)

// Codec tells the store how to handle the document type T.
type Codec[T any] struct {
	// New returns an empty document.
	New func() *T
	// Clone returns a copy that the change function can change. It must copy
	// everything that a change function can change.
	Clone func(*T) *T
	// Decode reads the bytes of a file.
	Decode func(data []byte) (*T, error)
	// Encode writes the bytes of a file. etag is the etag of the version that
	// the change started from. Old pods write this value to the file, and the
	// store keeps it for the same format.
	Encode func(doc *T, etag string) ([]byte, error)
	// SetEtag stores the etag of the storage in the document. The store calls
	// it only for a document that it owns and has not published yet.
	SetEtag func(doc *T, etag string)
}

// Entry is a published version of a document. Callers must not change it.
type Entry[T any] struct {
	Doc *T
	// Etag is the etag of the version from the storage. It is empty when the
	// document does not exist on the storage.
	Etag string
}

// Options change the details of a store. All fields are optional.
type Options struct {
	// Tracer returns the tracer of a call.
	Tracer func(ctx context.Context) trace.Tracer
	// PathAttrs returns the span attributes for a path, for example the ID of
	// the user.
	PathAttrs func(path string) []attribute.KeyValue
	// ReadTimeout limits a shared download.
	ReadTimeout time.Duration
}

// Store holds the documents of one kind.
type Store[T any] struct {
	storage metadata.Storage
	codec   Codec[T]
	opts    Options

	mu      sync.RWMutex
	entries map[string]*Entry[T]

	locks   sync.Map // path -> *sync.Mutex, only for writers
	flights singleflight.Group
}

// New returns a store for the documents of one kind.
func New[T any](storage metadata.Storage, codec Codec[T], opts ...Options) *Store[T] {
	s := &Store[T]{storage: storage, codec: codec, entries: map[string]*Entry[T]{}}
	if len(opts) > 0 {
		s.opts = opts[0]
	}
	if s.opts.ReadTimeout <= 0 {
		s.opts.ReadTimeout = defaultReadTimeout
	}
	return s
}

func (s *Store[T]) load(p string) *Entry[T] {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[p]
}

// publish replaces the entry of the path with next. It does this only when the
// current entry is still start, the entry that the call started from. Without
// this rule, a slow read that started before a write could publish an old
// document after the write. The rule is a compare and swap of the pointer,
// because etags have no order.
func (s *Store[T]) publish(p string, start, next *Entry[T]) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[p] != start {
		return false
	}
	s.entries[p] = next
	return true
}

// Peek returns the published entry of the path without a download. It returns
// nil when the store has no entry.
func (s *Store[T]) Peek(p string) *Entry[T] { return s.load(p) }

func (s *Store[T]) tracer(ctx context.Context) trace.Tracer {
	if s.opts.Tracer != nil {
		return s.opts.Tracer(ctx)
	}
	return appctx.GetTracerProvider(ctx).Tracer(tracerName)
}

func (s *Store[T]) startSpan(ctx context.Context, name, p string) (context.Context, trace.Span) {
	ctx, span := s.tracer(ctx).Start(ctx, name)
	if s.opts.PathAttrs != nil {
		span.SetAttributes(s.opts.PathAttrs(p)...)
	}
	return ctx, span
}

// Get returns the current version of the document. A missing document gives
// an empty document with an empty etag. A failed read returns the error and
// keeps the old entry.
//
// Parallel reads of one path that start from the same entry share one download. Each caller waits with its
// own context. The shared download does not end when one caller leaves.
func (s *Store[T]) Get(ctx context.Context, p string) (*Entry[T], error) {
	start := s.load(p)
	// The key holds the entry that the read starts from. A read that starts
	// after a write has a newer entry, so it does not join an older read that
	// can return an old body.
	ch := s.flights.DoChan(fmt.Sprintf("%s|%p", p, start), func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.ReadTimeout)
		defer cancel()
		e, err := s.fetch(fctx, p, start)
		if err != nil {
			return nil, err
		}
		s.publish(p, start, e)
		return e, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*Entry[T]), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetch downloads the document. It sends the etag of have, so that the storage
// answers 304 when the document did not change. It does not publish.
func (s *Store[T]) fetch(ctx context.Context, p string, have *Entry[T]) (*Entry[T], error) {
	ctx, span := s.startSpan(ctx, "Sync", p)
	defer span.End()

	req := metadata.DownloadRequest{Path: p}
	if have != nil && have.Etag != "" {
		req.IfNoneMatch = []string{have.Etag}
	}
	res, err := s.download(ctx, req)
	switch err.(type) {
	case nil:
	case errtypes.NotFound:
		span.SetStatus(codes.Ok, "")
		if have != nil && have.Etag == "" {
			return have, nil // still empty, keep the entry
		}
		return &Entry[T]{Doc: s.codec.New()}, nil
	case errtypes.NotModified:
		span.SetStatus(codes.Ok, "")
		if have == nil {
			return nil, fmt.Errorf("docstore: not modified without an etag: %s", p)
		}
		return have, nil
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	span.SetAttributes(attribute.Int("metadata.bytes", len(res.Content)))
	doc, err := s.codec.Decode(res.Content)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.AddEvent("decoded")
	s.codec.SetEtag(doc, res.Etag)
	span.SetStatus(codes.Ok, "")
	return &Entry[T]{Doc: doc, Etag: res.Etag}, nil
}

// download makes one conditional download. It repeats a transient error.
func (s *Store[T]) download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	bo := cas.NewBackoff()
	var err error
	for i := 0; i < maxReadAttempts; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		var res *metadata.DownloadResponse
		res, err = s.storage.Download(ctx, req)
		if err == nil || !cas.IsSyncTransient(err) || ctx.Err() != nil {
			return res, err
		}
		if werr := wait(ctx, bo.NextBackOff()); werr != nil {
			return nil, werr
		}
	}
	return nil, err
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lock waits for the write lock of the path. The lock only lowers the number
// of conflicts between writers of the same pod. Correctness does not depend on
// it. Readers never take it.
func (s *Store[T]) lock(ctx context.Context, p string) func() {
	v, _ := s.locks.LoadOrStore(p, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	// A lock span is only useful under a recorded parent span.
	if !trace.SpanFromContext(ctx).IsRecording() {
		mu.Lock()
		return mu.Unlock
	}
	_, span := s.startSpan(ctx, "Grab lock", p)
	mu.Lock()
	span.End()
	return mu.Unlock
}

// Update changes the document with fn.
//
// fn gets a private copy of the document. It reports whether it changed the
// copy. fn can run more than once, so it must give a correct result on each
// base, and it must start each call with empty results. After Update returns,
// the results of the last call of fn are valid.
//
// With a change, Update uploads the copy with the etag of its base. A conflict
// makes Update download the document again and run fn again. A transient error
// of the upload repeats the upload. If the first upload succeeded and only the
// answer got lost, the repeat gets a conflict, and the new download finds the
// change.
//
// When fn reports no change on a base from the cache, Update downloads the
// document and runs fn again. An old entry must not hide an item that is in the
// file. This download does not join a running read, because that read can
// have an old body.
func (s *Store[T]) Update(ctx context.Context, p string, fn func(doc *T) (changed bool, err error)) (err error) {
	// The lock span is a child of the span of the caller. The wait is not
	// part of the Persist span.
	unlock := s.lock(ctx, p)
	defer unlock()

	ctx, span := s.startSpan(ctx, "Persist", p)
	defer span.End()
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
	}()

	start := s.load(p)
	base := start
	fresh := false
	if base == nil {
		if base, err = s.fetch(ctx, p, nil); err != nil {
			return err
		}
		fresh = true
	}

	bo := cas.NewBackoff()
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		work := s.codec.Clone(base.Doc)
		changed, ferr := fn(work)
		if ferr != nil {
			return ferr
		}
		if !changed {
			if fresh {
				return nil
			}
			if base, err = s.fetch(ctx, p, base); err != nil {
				return err
			}
			fresh = true
			continue
		}

		next, uerr := s.upload(ctx, p, base, work)
		switch {
		case uerr == nil:
			s.publish(p, start, next)
			return nil
		case ctx.Err() != nil:
			return uerr
		case cas.IsConflict(uerr):
			span.AddEvent("conflict")
			if base, err = s.fetch(ctx, p, base); err != nil {
				return err
			}
			fresh = true
		case cas.IsTransientGRPCStatus(uerr):
			span.AddEvent("transient error, retry")
		default:
			return uerr
		}
		if werr := wait(ctx, bo.NextBackOff()); werr != nil {
			return werr
		}
	}
	return fmt.Errorf("docstore: giving up after %d tries: %s", maxWriteAttempts, p)
}

// upload writes doc with the etag condition of base.
func (s *Store[T]) upload(ctx context.Context, p string, base *Entry[T], doc *T) (*Entry[T], error) {
	data, err := s.codec.Encode(doc, base.Etag)
	if err != nil {
		return nil, err
	}
	req := metadata.UploadRequest{Path: p, Content: data, IfMatchEtag: base.Etag}
	if base.Etag == "" {
		// The document is new. The storage refuses the upload when another
		// writer created it first.
		req.IfMatchEtag = ""
		req.IfNoneMatch = []string{"*"}
		if err := s.storage.MakeDirIfNotExist(ctx, path.Dir(p)); err != nil {
			return nil, err
		}
	}
	res, err := s.storage.Upload(ctx, req)
	if err != nil {
		return nil, err
	}
	s.codec.SetEtag(doc, res.Etag)
	return &Entry[T]{Doc: doc, Etag: res.Etag}, nil
}

// Create writes an empty document when the document does not exist. It does
// nothing when the document exists. A conflict with another creator is no
// error.
func (s *Store[T]) Create(ctx context.Context, p string) error {
	unlock := s.lock(ctx, p)
	defer unlock()

	start := s.load(p)
	base := start
	if base == nil {
		var err error
		if base, err = s.fetch(ctx, p, nil); err != nil {
			return err
		}
	}
	if base.Etag != "" {
		return nil
	}
	next, err := s.upload(ctx, p, base, s.codec.New())
	if err != nil {
		if cas.IsConflict(err) {
			return nil // another writer created the document
		}
		return err
	}
	s.publish(p, start, next)
	return nil
}
