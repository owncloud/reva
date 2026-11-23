// Copyright 2018-2022 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package jsoncs3

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

// peakStorage delays each upload when slow is set, and records the highest
// number of uploads that run at the same time.
type peakStorage struct {
	metadata.Storage
	slow     atomic.Bool
	inflight atomic.Int32
	peak     atomic.Int32
}

func (p *peakStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if p.slow.Load() {
		n := p.inflight.Add(1)
		defer p.inflight.Add(-1)
		for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p.Storage.Upload(ctx, req)
}

// Break 1: the removals use MaxConcurrency 1 as their limit. They then run one
// after the other, and an unshare needs one more round trip than before.
// Break 2: the floor ignores a higher MaxConcurrency, or the removals have no
// limit, so a purge of a large space sends too many requests at the same time.
func TestRemoveSharesLimitsTheParallelUploads(t *testing.T) {
	for _, tc := range []struct {
		maxConcurrency int
		wantPeak       int32
	}{
		{maxConcurrency: 1, wantPeak: 3},
		{maxConcurrency: 3, wantPeak: 3},
		{maxConcurrency: 5, wantPeak: 5},
		{maxConcurrency: 0, wantPeak: 10},
	} {
		t.Run(fmt.Sprintf("MaxConcurrency=%d", tc.maxConcurrency), func(t *testing.T) {
			g := NewWithT(t)
			dir, err := os.MkdirTemp("", "jsoncs3-concurrency")
			g.Expect(err).ToNot(HaveOccurred())
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			disk, err := metadata.NewDiskStorage(dir)
			g.Expect(err).ToNot(HaveOccurred())
			ps := &peakStorage{Storage: disk}
			m, err := New(ps, nil, 0, nopStream{}, tc.maxConcurrency)
			g.Expect(err).ToNot(HaveOccurred())
			f := &batchFixture{storage: disk, m: m}
			// 20 users give 20 received.json files, more than each limit.
			for n := 0; n < 20; n++ {
				f.share(t, ctxOf("admin"), n, userGrantee(fmt.Sprintf("user%d", n)), nil)
			}
			shares, err := m.ListShares(ctxOf("admin"), nil)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(shares).To(HaveLen(20))

			ps.slow.Store(true)
			_, err = m.removeShares(ctxOf("admin"), shares, false)
			g.Expect(err).ToNot(HaveOccurred())

			g.Expect(ps.peak.Load()).To(Equal(tc.wantPeak))
		})
	}
}
