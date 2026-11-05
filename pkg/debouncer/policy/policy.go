package policy

import (
	"time"
)

// Policy represents a trigger policy for the debouncer to use.
// The debouncer should queue tasks until the policy triggers, then the
// debouncer can run through the tasks.
//
// The MarkTaskAdded and MarkTaskRemoved methods are used to help the
// policy to decide whether to send the trigger now or wait a bit longer.
// For example, a time-based policy can queue tasks for 2 minutes before
// triggering, and can extend or reset that time period if tasks are being
// added to the queue
//
// The WaitForTrigger will return a channel so other goroutines can wait on
// it. Note that the expectation is the policy will trigger only once.
// Once the policy triggers, consider it unusable for the rest of the
// execution. Create a new instance if you need it.
type Policy interface {
	// MarkTaskAdded will notify the policy that a new task has been added
	// to the queue.
	// Return true if it's acknowledged, false otherwise
	MarkTaskAdded() bool
	// MarkTaskRemoved will notify the policy that a new task has been
	// removed from the queue.
	// Return true if it's acknowledged, false otherwise
	MarkTaskRemoved() bool
	// WaitForTrigger will return a receiver channel to wait for the policy
	// to trigger. The channel will send the time when the policy triggered,
	// and then the channel will be closed.
	// Note that multiple goroutines might be waiting here.
	// The recommendation is to use a shared channel, so all the goroutines
	// wait on the same channel. Once the channel is closed, all the
	// goroutines can proceed. Note that only one goroutine will receive
	// the time.
	WaitForTrigger() <-chan time.Time
	// GetId returns the ID of this instance. It must be unique, so multiple
	// instances from the same policy type must return different IDs.
	// The recommendation is to use the instance type followed by a random
	// number, such as "TimedWithReset_123987"
	GetId() string
	// GetTracingData returns additional data that will be used for tracing.
	// Consider this data as public information. This data is intended to
	// be use purely for informational purposes. You can return nil if
	// there isn't any data to be published.
	GetTracingData() map[string]string
}
