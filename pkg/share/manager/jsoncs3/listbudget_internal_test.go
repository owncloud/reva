package jsoncs3

// A listing has a total time budget. A slow or dead storage must not hold a
// login for the sum of many retries. When the budget is over, the listing
// returns an error. A partial list would hide shares without a sign.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

// slowStorage delays each Download. The delay ends when the context ends.
type slowStorage struct {
	metadata.Storage
	delay     atomic.Int64 // nanoseconds
	downloads atomic.Int32
}

func (s *slowStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	s.downloads.Add(1)
	if d := time.Duration(s.delay.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Storage.Download(ctx, req)
}

func TestListReceivedSharesReturnsAnErrorWhenTheBudgetIsOver(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)

	slow := &slowStorage{Storage: f.storage}
	m, err := New(slow, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	m.ListTimeout = 300 * time.Millisecond
	slow.delay.Store(int64(100 * time.Millisecond))

	// Ten groups, each read takes 100 ms: about one second in total.
	groups := []string{"group1", "g2", "g3", "g4", "g5", "g6", "g7", "g8", "g9", "g10"}
	start := time.Now()
	received, err := m.ListReceivedShares(ctxOf("einstein", groups...), nil, nil)

	g.Expect(err).To(HaveOccurred(), "a listing that ran out of time must not look like a complete list")
	g.Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue(), "got %v", err)
	g.Expect(received).To(BeNil())
	g.Expect(time.Since(start)).To(BeNumerically("<", 800*time.Millisecond))
}

func TestListReceivedSharesWithinTheBudgetIsComplete(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)

	slow := &slowStorage{Storage: f.storage}
	m, err := New(slow, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	m.ListTimeout = 5 * time.Second

	g.Expect(receivedIDs(t, m, "einstein", "group1")).To(HaveLen(1))
}

func TestTheDefaultBudgetIsTenSeconds(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	g.Expect(f.m.ListTimeout).To(Equal(10 * time.Second))
}
