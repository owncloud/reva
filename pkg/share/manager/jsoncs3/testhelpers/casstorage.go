package helpers

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// CASStorage is an in-memory metadata.Storage with a correct CAS check. The
// benchmarks and the tests of the store use it.
//
// metadata.Disk cannot prove that no change gets lost: it accepts an If-Match
// on a missing file, and two writes of the same size can get the same etag.
// CASStorage checks the conditions and writes the file in one step under one
// lock. It refuses an If-Match on a missing file, and it gives each write a
// new etag. This is the behavior of the decomposedfs storage provider.
type CASStorage struct {
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

var _ metadata.Storage = (*CASStorage)(nil)

// NewCASStorage returns an empty CASStorage.
func NewCASStorage() *CASStorage {
	return &CASStorage{files: map[string]casFile{}, dirs: map[string]bool{"/": true}}
}

func casPath(p string) string {
	return path.Join("/", p)
}

func (s *CASStorage) Backend() string { return "cas-memory" }

func (s *CASStorage) Init(context.Context, string) error { return nil }

func (s *CASStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
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

func (s *CASStorage) Download(_ context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
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

func (s *CASStorage) SimpleUpload(ctx context.Context, p string, content []byte) error {
	_, err := s.Upload(ctx, metadata.UploadRequest{Path: p, Content: content})
	return err
}

func (s *CASStorage) SimpleDownload(ctx context.Context, p string) ([]byte, error) {
	res, err := s.Download(ctx, metadata.DownloadRequest{Path: p})
	if err != nil {
		return nil, err
	}
	return res.Content, nil
}

func (s *CASStorage) Delete(_ context.Context, p string) error {
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

func (s *CASStorage) Stat(_ context.Context, p string) (*provider.ResourceInfo, error) {
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

func (s *CASStorage) ReadDir(ctx context.Context, p string) ([]string, error) {
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

func (s *CASStorage) ListDir(_ context.Context, p string) ([]*provider.ResourceInfo, error) {
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

func (s *CASStorage) CreateSymlink(context.Context, string, string) error {
	return errtypes.NotSupported("symlinks")
}

func (s *CASStorage) ResolveSymlink(context.Context, string) (string, error) {
	return "", errtypes.NotSupported("symlinks")
}

// MakeDirIfNotExist creates the folder and its parents.
func (s *CASStorage) MakeDirIfNotExist(_ context.Context, p string) error {
	p = casPath(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	for d := p; d != "/" && !s.dirs[d]; d = path.Dir(d) {
		s.dirs[d] = true
	}
	return nil
}
