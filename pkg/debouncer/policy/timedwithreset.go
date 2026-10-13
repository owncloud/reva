package policy

import (
	"math/rand"
	"strconv"
	"sync/atomic"
	"time"
)

// TimedWithReset implements a debouncer policy.
// The policy will trigger after the specified duration passes. If the policy
// hasn't been triggered yet, the duration will be reset each time the
// MarkTaskAdded is called.
type TimedWithReset struct {
	id          string
	timer       *time.Timer
	triggerChan chan time.Time
	duration    time.Duration
	done        *atomic.Bool
}

// NewTimedWithReset creates a new instance of TimedWithReset.
// The timer will start immediately.
// Note that this implementation won't provide extreme accurate timing,
// so some minor delays (milliseconds at most) are expected. The expected
// usage should have at least a 10 seconds duration for the timer (tests use
// a 1 second timer), which should be enough to interact with this policy
// before the timer goes off.
func NewTimedWithReset(dur time.Duration) *TimedWithReset {
	timer := time.NewTimer(dur)
	triggerChan := make(chan time.Time)
	done := &atomic.Bool{}
	id := "TimedWithReset_" + strconv.FormatUint(rand.Uint64(), 10)

	go func() {
		defer timer.Stop()
		t := <-timer.C
		done.Store(true)
		triggerChan <- t
		close(triggerChan)
	}()

	return &TimedWithReset{
		id:          id,
		timer:       timer,
		triggerChan: triggerChan,
		duration:    dur,
		done:        done,
	}
}

// MarkTaskAdded will return true if the policy hasn't triggered yet
// and the timer has been reset, false otherwise.
func (tr *TimedWithReset) MarkTaskAdded() bool {
	if tr.done.Load() == false {
		return tr.timer.Reset(tr.duration)
	}
	return false
}

// MarkTaskRemoved has no effect and it will always return true
func (tr *TimedWithReset) MarkTaskRemoved() bool {
	return true
}

// WaitForTrigger will return a channel in order to wait for the policy
// to trigger. The channel will be the same for all the calls to this method
// (the channel will be shared among all the callers)
// The channel will send the time when the trigger happens, however, since
// the channel is shared among all the waiting goroutines, only one of them
// will receive the time.
// The channel will be closed once the policy triggers, so all the waiting
// goroutines can continue afterwards.
func (tr *TimedWithReset) WaitForTrigger() <-chan time.Time {
	return tr.triggerChan
}

// GetId returns the ID of this instance. It will return "TimedWithReset_"
// followed by a random number.
func (tr *TimedWithReset) GetId() string {
	return tr.id
}

// GetTracingData will return the duration used by this instance, under
// the key "duration", using the "Duration.String()" method.
func (tr *TimedWithReset) GetTracingData() map[string]string {
	return map[string]string{
		"duration": tr.duration.String(),
	}
}
