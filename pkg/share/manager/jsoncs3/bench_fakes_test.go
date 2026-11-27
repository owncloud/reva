package jsoncs3_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	microevents "go-micro.dev/v4/events"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

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
