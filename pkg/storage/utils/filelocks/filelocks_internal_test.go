// Copyright 2018-2021 CERN
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

package filelocks

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
)

// TestGetMutexedFlock_Exclusive proves getMutexedFlock never admits two
// concurrent holders for the same path.
func TestGetMutexedFlock_Exclusive(t *testing.T) {
	const (
		goroutines = 100
		iterations = 200
	)
	path := "mutexed-flock-exclusivity-probe"

	var held int32
	var overlapSeen int32
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				var l *flock.Flock
				for l == nil {
					l = getMutexedFlock(path)
				}

				if atomic.AddInt32(&held, 1) > 1 {
					atomic.StoreInt32(&overlapSeen, 1)
				}

				atomic.AddInt32(&held, -1)
				releaseMutexedFlock(path)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(0), overlapSeen, "two goroutines held the local gate for %q at the same time", path)
}

func TestAcquireReadLock_Errors(t *testing.T) {
	l1, err := acquireLock(t.Context(), "", false)
	assert.Nil(t, l1)
	assert.Equal(t, err, ErrPathEmpty)

	file, fin, _ := FileFactory()
	defer fin()

	l2, err := acquireLock(t.Context(), file, false)
	assert.NotNil(t, l2)
	assert.Nil(t, err)

	l3, err := acquireLock(t.Context(), file, false)
	assert.Nil(t, l3)
	assert.Equal(t, err, ErrAcquireLockFailed)
}

func TestAcquireWriteLock_DoesNotWedgeAfterExternalContentionClears(t *testing.T) {
	file, fin, _ := FileFactory()
	defer fin()

	// speed up the retry loops for this test
	origCycles, origFactor := _lockCyclesValue, _lockCycleDurationFactor
	_lockCyclesValue, _lockCycleDurationFactor = 3, 1
	defer func() { _lockCyclesValue, _lockCycleDurationFactor = origCycles, origFactor }()

	// simulate a real external OS-level holder of the lock (e.g. another
	// process), independent of this package's local bookkeeping.
	external := flock.New(FlockFile(file))
	ok, err := external.TryLock()
	assert.True(t, ok)
	assert.Nil(t, err)

	// contended: must fail while the external holder is locked.
	l1, err := acquireLock(t.Context(), file, true)
	assert.Nil(t, l1)
	assert.Equal(t, ErrAcquireLockFailed, err)

	// release the external holder: nothing real is locking the file anymore.
	assert.Nil(t, external.Unlock())

	// must now succeed, since no one holds the real lock. If this fails,
	// acquireLock's earlier failed attempt leaked its entry in _localLocks
	// and every subsequent call for this path is permanently wedged.
	l2, err := acquireLock(t.Context(), file, true)
	assert.NotNil(t, l2, "acquireLock is permanently wedged after a transient external lock cleared")
	assert.Nil(t, err)
}

// utils

func FileFactory() (string, func(), error) {
	fu := func() {}
	tmpFile, err := os.CreateTemp(os.TempDir(), "flock")
	if err != nil {
		return "", fu, err
	}

	fu = func() {
		_ = os.Remove(tmpFile.Name())
	}

	err = tmpFile.Close()
	if err != nil {
		return "", fu, err
	}

	return tmpFile.Name(), fu, err
}
