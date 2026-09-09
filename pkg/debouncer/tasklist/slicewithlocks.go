package tasklist

import (
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
)

// SliceWithLocks implements a TaskList using a slice of tasks and locks
type SliceWithLocks struct {
	id      string
	list    []InternalTask
	rwmutex *sync.RWMutex
	frozen  *atomic.Bool
}

// NewSliceWithLocks creates a new SliceWithLocks instance
func NewSliceWithLocks() TaskList {
	rw := &sync.RWMutex{}
	frozen := &atomic.Bool{}

	return &SliceWithLocks{
		id:      "SliceWithLocks_" + strconv.FormatUint(rand.Uint64(), 10),
		list:    make([]InternalTask, 0),
		rwmutex: rw,
		frozen:  frozen,
	}
}

// AddToList adds the task to the list. Returns true if added, or false if
// the list is frozen
func (swl *SliceWithLocks) AddToList(task InternalTask) bool {
	swl.rwmutex.Lock()
	defer swl.rwmutex.Unlock()

	if swl.frozen.Load() == true {
		return false
	}

	swl.list = append(swl.list, task)
	return true
}

// Freeze will freeze the list so no new task is added.
// You can freeze the list multiple times, but you can't revert (melt) it
func (swl *SliceWithLocks) Freeze() {
	// Use a read lock because we don't want to change the state while
	// adding a new task to the list.
	swl.rwmutex.RLock()
	defer swl.rwmutex.RUnlock()

	_ = swl.frozen.CompareAndSwap(false, true)
}

// IsFrozen returns whether the list has been frozen or not
func (swl *SliceWithLocks) IsFrozen() bool {
	return swl.frozen.Load()
}

// ToSlice will return a slice of tasks. It will return a copy of the backed
// slice, so both the original and the copy can be modified independently
// (although you shouldn't modify the returned list)
// This method will return a shallow copy. The tasks are expected to be
// pointers, so modifying the tasks in any of the lists (either the original
// or the returned copy) will affect both.
// It's recommended to use this method after the freeze to ensure no new task
// is added later and we're operating over an old list.
func (swl *SliceWithLocks) ToSlice() []InternalTask {
	swl.rwmutex.RLock()
	defer swl.rwmutex.RUnlock()

	out := make([]InternalTask, len(swl.list))
	_ = copy(out, swl.list)
	return out
}

// GetId will return the id of this instance. This will return
// "SliceWithLocks_" followed by a random number.
func (swl *SliceWithLocks) GetId() string {
	return swl.id
}

// GetTracingData will return nil
func (swl *SliceWithLocks) GetTracingData() map[string]string {
	return nil
}
