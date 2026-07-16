// Copyright 2022 CERN
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

package ldap

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// fakeConn is a minimal ldap.Client double used to test pool bookkeeping without a real LDAP
// server. It embeds a nil ldap.Client so it satisfies the interface; only Close is overridden,
// which is the only method the pool itself calls on connections it manages.
type fakeConn struct {
	ldap.Client
	closed bool
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

// newTestPool returns a pool with a fake, network-free dial function and a counter of how many
// times it was called.
func newTestPool(size int, timeout time.Duration) (*ConnPool, *int32) {
	var dialCount int32
	p := NewLDAPPool(Config{PoolSize: size, PoolCheckoutTimeout: timeout})
	p.dial = func(Config) (ldap.Client, error) {
		atomic.AddInt32(&dialCount, 1)
		return &fakeConn{}, nil
	}
	return p, &dialCount
}

func TestConnPoolLazyConstruction(t *testing.T) {
	_, dialCount := newTestPool(2, time.Second)
	if got := atomic.LoadInt32(dialCount); got != 0 {
		t.Fatalf("expected no dials on construction, got %d", got)
	}
}

func TestConnPoolCheckoutReusesReturnedConnection(t *testing.T) {
	p, dialCount := newTestPool(2, time.Second)

	conn, err := p.checkout()
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	p.release(conn, nil)

	conn2, err := p.checkout()
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	if conn2 != conn {
		t.Fatalf("expected the returned connection to be reused")
	}
	if got := atomic.LoadInt32(dialCount); got != 1 {
		t.Fatalf("expected exactly 1 dial, got %d", got)
	}
}

func TestConnPoolEvictsUnhealthyConnection(t *testing.T) {
	p, dialCount := newTestPool(2, time.Second)

	conn, err := p.checkout()
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	networkErr := ldap.NewError(ldap.ErrorNetwork, errors.New("boom"))
	p.release(conn, networkErr)

	if !conn.(*fakeConn).closed {
		t.Fatalf("expected the unhealthy connection to be closed")
	}

	if _, err := p.checkout(); err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	if got := atomic.LoadInt32(dialCount); got != 2 {
		t.Fatalf("expected a redial after eviction, got %d dials", got)
	}
}

func TestConnPoolExhaustionTimesOut(t *testing.T) {
	p, _ := newTestPool(1, 50*time.Millisecond)

	if _, err := p.checkout(); err != nil {
		t.Fatalf("checkout failed: %v", err)
	}

	start := time.Now()
	_, err := p.checkout()
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected ErrPoolExhausted, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("expected checkout to wait for the timeout, only waited %v", elapsed)
	}
}

func TestConnPoolCloseDrainsIdleAndRejectsCheckout(t *testing.T) {
	p, _ := newTestPool(1, time.Second)

	conn, err := p.checkout()
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	p.release(conn, nil)

	if err := p.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if !conn.(*fakeConn).closed {
		t.Fatalf("expected the idle connection to be closed by Close")
	}
	if _, err := p.checkout(); !errors.Is(err, errPoolClosed) {
		t.Fatalf("expected errPoolClosed, got %v", err)
	}
}

func TestConnPoolConcurrentCheckoutRelease(t *testing.T) {
	p, dialCount := newTestPool(4, time.Second)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			conn, err := p.checkout()
			if err != nil {
				t.Errorf("checkout failed: %v", err)
				return
			}
			p.release(conn, nil)
		})
	}
	wg.Wait()

	if got := atomic.LoadInt32(dialCount); got > 4 {
		t.Fatalf("expected at most 4 dials (pool size), got %d", got)
	}
}
