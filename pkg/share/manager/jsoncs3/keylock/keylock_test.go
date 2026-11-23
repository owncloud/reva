// Copyright 2026 ownCloud GmbH <mail@owncloud.com>
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

package keylock_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/keylock"
	"github.com/rs/zerolog"
)

func TestLockIsExclusivePerKey(t *testing.T) {
	var m keylock.Map
	ctx := context.Background()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		inside  int
		maxSeen int
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := m.Lock(ctx, "a")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			inside++
			if inside > maxSeen {
				maxSeen = inside
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("expected at most one holder at a time, saw %d", maxSeen)
	}
}

func TestLockKeysAreIndependent(t *testing.T) {
	var m keylock.Map
	ctx := context.Background()

	unlockA, err := m.Lock(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockA()

	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	unlockB, err := m.Lock(cctx, "b")
	if err != nil {
		t.Fatalf("locking an unrelated key must not wait: %v", err)
	}
	unlockB()
}

func TestLockReturnsWhenContextIsCanceledWhileWaiting(t *testing.T) {
	var m keylock.Map
	unlock, err := m.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		u, err := m.Lock(ctx, "a")
		if u != nil {
			u()
		}
		errc <- err
	}()

	select {
	case err := <-errc:
		t.Fatalf("waiter acquired a held lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not return after its context was canceled")
	}
}

func TestLockWithDoneContextFailsEvenIfFree(t *testing.T) {
	var m keylock.Map
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for i := 0; i < 100; i++ {
		unlock, err := m.Lock(ctx, "a")
		if !errors.Is(err, context.Canceled) || unlock != nil {
			t.Fatalf("expected context.Canceled and no unlock func, got %v", err)
		}
	}

	// the failed attempts must not have left the lock held
	unlock, err := m.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestUnlockHandsOverToWaiter(t *testing.T) {
	var m keylock.Map
	unlock, err := m.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		u, err := m.Lock(context.Background(), "a")
		if err == nil {
			u()
		}
		close(done)
	}()

	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not acquire the lock after unlock")
	}
}

func TestDoubleUnlockIsANoOpAndLogs(t *testing.T) {
	var buf syncBuffer
	logger := zerolog.New(&buf)
	ctx := appctx.WithLogger(context.Background(), &logger)

	var m keylock.Map
	unlock, err := m.Lock(ctx, "the-key")
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		unlock()
	}()

	select {
	case p := <-done:
		if p != nil {
			t.Fatalf("second unlock must not panic, got %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second unlock blocked")
	}

	out := buf.String()
	if !strings.Contains(out, "keylock: unlock of unlocked key") || !strings.Contains(out, `"key":"the-key"`) {
		t.Fatalf("expected an error log with the key, got %q", out)
	}

	// the lock is still usable afterwards
	lctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	u, err := m.Lock(lctx, "the-key")
	if err != nil {
		t.Fatalf("lock not acquirable after double unlock: %v", err)
	}
	u()
}

func TestDoubleUnlockDoesNotReleaseAnotherHoldersLock(t *testing.T) {
	var m keylock.Map
	first, err := m.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	first()

	second, err := m.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer second()

	stale := make(chan any, 1)
	go func() {
		defer func() { stale <- recover() }()
		first()
	}()
	select {
	case p := <-stale:
		if p != nil {
			t.Fatalf("stale unlock must not panic, got %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale unlock blocked")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if u, err := m.Lock(ctx, "a"); err == nil {
		u()
		t.Fatal("the stale unlock released the current holder's lock")
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writes and reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
