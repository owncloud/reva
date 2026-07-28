package action

import (
	"context"
	"math/rand"
	"strconv"

	"github.com/owncloud/reva/v2/pkg/debouncer/tasklist"
)

// ChooseLast implements a debouncer action that will always run the last
// task of the list, which is expected to be the most recent one.
type ChooseLast struct {
	id string
}

// NewChooseLast will return a new instance
func NewChooseLast() *ChooseLast {
	return &ChooseLast{
		id: "ChooseLast_" + strconv.FormatUint(rand.Uint64(), 10),
	}
}

// RunTasks will run the last task of the provided list. If the list is empty
// a ErrEmptyList will be returned.
func (cl *ChooseLast) RunTasks(ctx context.Context, tasks []tasklist.InternalTask) error {
	if len(tasks) <= 0 {
		return ErrEmptyList
	}

	chosenTask := tasks[len(tasks)-1]
	return chosenTask.OriginalTask.Execute(ctx)
}

// GetId will return the id of this instance. It will return "ChooseLast_"
// followed by a random number.
func (cl *ChooseLast) GetId() string {
	return cl.id
}

// GetTracingData will return nil
func (cl *ChooseLast) GetTracingData() map[string]string {
	return nil
}
