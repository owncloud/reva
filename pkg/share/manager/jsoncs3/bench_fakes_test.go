package jsoncs3_test

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	microevents "go-micro.dev/v4/events"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// casStorage is an in-memory metadata.Storage with a correct CAS check. It is
// the storage of the benchmarks.
//
// metadata.Disk cannot prove that no change gets lost: it accepts an If-Match
// on a missing file, and two writes of the same size can get the same etag.
// casStorage checks the conditions and writes the file in one step under one
// lock. It refuses an If-Match on a missing file, and it gives each write a
// new etag. This is the behavior of the decomposedfs storage provider.
type casStorage struct {
	mu    sync.Mutex
	files map[string]casFile
	dirs  map[string]bool
	next  uint64
}

type casFile struct {
	content []byte
	etag    string
	mtime   time.Time
}

var _ metadata.Storage = (*casStorage)(nil)

func newCASStorage() *casStorage {
	return &casStorage{files: map[string]casFile{}, dirs: map[string]bool{"/": true}}
}

func casPath(p string) string {
	return path.Join("/", p)
}

func (s *casStorage) Backend() string { return "cas-memory" }

func (s *casStorage) Init(context.Context, string) error { return nil }

func (s *casStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	p := casPath(req.Path)
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, exists := s.files[p]
	if req.IfMatchEtag != "" && (!exists || cur.etag != req.IfMatchEtag) {
		return nil, errtypes.PreconditionFailed("etag mismatch")
	}
	for _, v := range req.IfNoneMatch {
		if v == "*" && exists {
			return nil, errtypes.AlreadyExists(p)
		}
		if exists && v == cur.etag {
			return nil, errtypes.Aborted("etag matches")
		}
	}
	if !req.IfUnmodifiedSince.IsZero() && exists && cur.mtime.After(req.IfUnmodifiedSince) {
		return nil, errtypes.PreconditionFailed("modified")
	}

	s.next++
	f := casFile{
		content: append([]byte(nil), req.Content...),
		etag:    strconv.FormatUint(s.next, 10),
		mtime:   time.Now(),
	}
	if !req.MTime.IsZero() {
		f.mtime = req.MTime
	}
	s.files[p] = f
	return &metadata.UploadResponse{Etag: f.etag}, nil
}

func (s *casStorage) Download(_ context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	p := casPath(req.Path)
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.files[p]
	if !ok {
		return nil, errtypes.NotFound(p)
	}
	for _, v := range req.IfNoneMatch {
		if v == f.etag {
			return nil, errtypes.NotModified(p)
		}
	}
	return &metadata.DownloadResponse{Content: append([]byte(nil), f.content...), Etag: f.etag, Mtime: f.mtime}, nil
}

func (s *casStorage) SimpleUpload(ctx context.Context, p string, content []byte) error {
	_, err := s.Upload(ctx, metadata.UploadRequest{Path: p, Content: content})
	return err
}

func (s *casStorage) SimpleDownload(ctx context.Context, p string) ([]byte, error) {
	res, err := s.Download(ctx, metadata.DownloadRequest{Path: p})
	if err != nil {
		return nil, err
	}
	return res.Content, nil
}

func (s *casStorage) Delete(_ context.Context, p string) error {
	p = casPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[p]; ok {
		delete(s.files, p)
		return nil
	}
	if s.dirs[p] {
		delete(s.dirs, p)
		for name := range s.files {
			if strings.HasPrefix(name, p+"/") {
				delete(s.files, name)
			}
		}
		return nil
	}
	return errtypes.NotFound(p)
}

func (s *casStorage) Stat(_ context.Context, p string) (*provider.ResourceInfo, error) {
	p = casPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.files[p]; ok {
		return &provider.ResourceInfo{
			Type:  provider.ResourceType_RESOURCE_TYPE_FILE,
			Path:  p,
			Name:  path.Base(p),
			Etag:  f.etag,
			Size:  uint64(len(f.content)),
			Mtime: utils.TimeToTS(f.mtime),
		}, nil
	}
	if s.dirs[p] {
		return &provider.ResourceInfo{Type: provider.ResourceType_RESOURCE_TYPE_CONTAINER, Path: p, Name: path.Base(p)}, nil
	}
	return nil, errtypes.NotFound(p)
}

func (s *casStorage) ReadDir(ctx context.Context, p string) ([]string, error) {
	infos, err := s.ListDir(ctx, p)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Path)
	}
	return names, nil
}

func (s *casStorage) ListDir(_ context.Context, p string) ([]*provider.ResourceInfo, error) {
	p = casPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirs[p] {
		return nil, errtypes.NotFound(p)
	}
	children := map[string]provider.ResourceType{}
	for name := range s.files {
		if path.Dir(name) == p {
			children[name] = provider.ResourceType_RESOURCE_TYPE_FILE
		}
	}
	for name := range s.dirs {
		if name != p && path.Dir(name) == p {
			children[name] = provider.ResourceType_RESOURCE_TYPE_CONTAINER
		}
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := make([]*provider.ResourceInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, &provider.ResourceInfo{Type: children[name], Path: name, Name: path.Base(name)})
	}
	return infos, nil
}

func (s *casStorage) CreateSymlink(context.Context, string, string) error {
	return errtypes.NotSupported("symlinks")
}

func (s *casStorage) ResolveSymlink(context.Context, string) (string, error) {
	return "", errtypes.NotSupported("symlinks")
}

// MakeDirIfNotExist creates the folder and its parents.
func (s *casStorage) MakeDirIfNotExist(_ context.Context, p string) error {
	p = casPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	for d := p; d != "/" && !s.dirs[d]; d = path.Dir(d) {
		s.dirs[d] = true
	}
	return nil
}

// callCounts holds the number of storage calls by type.
type callCounts struct {
	download200, download304, download404, downloadErr atomic.Int64
	upload, uploadConflict, uploadErr                  atomic.Int64
	other                                              atomic.Int64
	events                                             atomic.Int64
}

func (c *callCounts) reset() {
	for _, v := range []*atomic.Int64{&c.download200, &c.download304, &c.download404, &c.downloadErr,
		&c.upload, &c.uploadConflict, &c.uploadErr, &c.other, &c.events} {
		v.Store(0)
	}
}

// total is the number of storage calls. It does not include the events.
func (c *callCounts) total() int64 {
	return c.download200.Load() + c.download304.Load() + c.download404.Load() + c.downloadErr.Load() +
		c.upload.Load() + c.uploadConflict.Load() + c.uploadErr.Load() + c.other.Load()
}

// report adds the calls for each operation to the benchmark result.
func (c *callCounts) report(b *testing.B, ops int) {
	if ops == 0 {
		return
	}
	per := func(v int64) float64 { return float64(v) / float64(ops) }
	b.ReportMetric(per(c.download200.Load()), "dl200/op")
	b.ReportMetric(per(c.download304.Load()), "dl304/op")
	b.ReportMetric(per(c.download404.Load()), "dl404/op")
	b.ReportMetric(per(c.downloadErr.Load()), "dlerr/op")
	b.ReportMetric(per(c.upload.Load()), "upload/op")
	b.ReportMetric(per(c.uploadConflict.Load()), "conflict/op")
	b.ReportMetric(per(c.uploadErr.Load()), "uperr/op")
	b.ReportMetric(per(c.other.Load()), "other/op")
	b.ReportMetric(per(c.total()), "calls/op")
	b.ReportMetric(per(c.events.Load()), "events/op")
}

// countingStorage counts the calls of the wrapped storage and waits for each
// call. Methods that it does not override go to the wrapped storage without a
// count and without a wait.
type countingStorage struct {
	metadata.Storage
	rtt    atomic.Int64 // time.Duration
	counts *callCounts
}

func (s *countingStorage) wait() {
	if d := time.Duration(s.rtt.Load()); d > 0 {
		time.Sleep(d)
	}
}

func (s *countingStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	s.wait()
	res, err := s.Storage.Download(ctx, req)
	switch err.(type) {
	case nil:
		s.counts.download200.Add(1)
	case errtypes.NotModified:
		s.counts.download304.Add(1)
	case errtypes.NotFound:
		s.counts.download404.Add(1)
	default:
		s.counts.downloadErr.Add(1)
	}
	return res, err
}

func (s *countingStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	s.wait()
	res, err := s.Storage.Upload(ctx, req)
	switch err.(type) {
	case nil:
		s.counts.upload.Add(1)
	case errtypes.Aborted, errtypes.PreconditionFailed, errtypes.AlreadyExists, errtypes.TooEarly:
		s.counts.uploadConflict.Add(1)
	default:
		s.counts.uploadErr.Add(1)
	}
	return res, err
}

func (s *countingStorage) Stat(ctx context.Context, path string) (*provider.ResourceInfo, error) {
	s.wait()
	s.counts.other.Add(1)
	return s.Storage.Stat(ctx, path)
}

func (s *countingStorage) MakeDirIfNotExist(ctx context.Context, name string) error {
	s.wait()
	s.counts.other.Add(1)
	return s.Storage.MakeDirIfNotExist(ctx, name)
}

func (s *countingStorage) ListDir(ctx context.Context, path string) ([]*provider.ResourceInfo, error) {
	s.wait()
	s.counts.other.Add(1)
	return s.Storage.ListDir(ctx, path)
}

func (s *countingStorage) Delete(ctx context.Context, path string) error {
	s.wait()
	s.counts.other.Add(1)
	return s.Storage.Delete(ctx, path)
}

// countingStream is an event stream that only counts the published events.
// Consume returns a channel that never gets an event. events.Consume reads
// the channel in a loop without a check for a closed channel, so the channel
// must stay open.
type countingStream struct {
	counts *callCounts
	never  chan microevents.Event
}

func newCountingStream(counts *callCounts) *countingStream {
	return &countingStream{counts: counts, never: make(chan microevents.Event)}
}

func (s *countingStream) Publish(string, interface{}, ...microevents.PublishOption) error {
	s.counts.events.Add(1)
	return nil
}

func (s *countingStream) Consume(string, ...microevents.ConsumeOption) (<-chan microevents.Event, error) {
	return s.never, nil
}
