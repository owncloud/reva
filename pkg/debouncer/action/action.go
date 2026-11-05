package action

import (
	"context"
	"errors"

	"github.com/owncloud/reva/v2/pkg/debouncer/tasklist"
)

var (
	ErrEmptyList = errors.New("Task list is empty")
)

// Action represents the action that the debouncer needs to perform
// over the provided task list.
// This action usually involves picking the "right" task, run it and
// return the error if any.
// Choosing the "right" task can be as easy as picking the first or last
// task of the list and run it, or more complex such as gathering info from
// all the tasks and create a new task based on the aggregated info.
// The action isn't limited to choosing just one task, and it can choose
// and run multiple tasks if needed (although not recommended).
type Action interface {
	// RunTasks perform an action over the provided task list. This usually
	// involves picking one task of the list and run it.
	// It's expected that RunTasks will be executed in its own goroutine,
	// using a new context.
	RunTasks(ctx context.Context, tasks []tasklist.InternalTask) error
	// GetId returns the ID of this instance. It must be unique, so multiple
	// instances from the same action type must return different IDs.
	// The recommendation is to use the instance type followed by a random
	// number, such as "ChooseLast_123987"
	GetId() string
	// GetTracingData returns additional data that will be used for tracing.
	// Consider this data as public information. This data is intended to
	// be use purely for informational purposes. You can return nil if
	// there isn't any data to be published.
	GetTracingData() map[string]string
}
