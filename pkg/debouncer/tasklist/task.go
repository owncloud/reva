package tasklist

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// Task represents a task that has been queued in the debouncer.
// The task can be anything, from printing a message to overwriting data
// in external services or indexing a space.
// The general expectation is that the same or similar tasks are queued
// together, and only one of them will be executed. This can be changed
// based on the configured policies and actions of the debouncer.
type Task interface {
	// ExposeData exposes some task data as key-value pairs. This is
	// tightly coupled with the Action in the debouncer, and it's expected
	// that the Action uses this information to decide whether to run
	// this Task or not. Some basic Actions might not need this info.
	ExposeData() map[string]string
	// Execute will run the task (such as overwriting a value in an
	// external service) in the provided context. Note that the context
	// will be new and it might not have some required context
	// (authentication info, for example), so you might need to add the
	// required information.
	Execute(ctx context.Context) error
}

type InternalTask struct {
	SpanContext  trace.SpanContext
	OriginalTask Task
}

func NewInternalTaskFromTask(ctx context.Context, t Task) InternalTask {
	return InternalTask{
		SpanContext:  trace.SpanContextFromContext(ctx),
		OriginalTask: t,
	}
}
