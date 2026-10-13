package debouncer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/owncloud/reva/v2/pkg/debouncer/action"
	"github.com/owncloud/reva/v2/pkg/debouncer/policy"
	"github.com/owncloud/reva/v2/pkg/debouncer/tasklist"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	TracerName = "github.com/owncloud/reva/v2/pkg/debouncer"
)

var (
	ErrQueueAlreadyCreated = errors.New("queue already created")
	ErrQueueFailedAdd      = errors.New("task failed to be added to the queue")
)

// PolicyFactory is a function to create Policies. The function is expected
// to return the same policy each time it's called, but it MUST return
// different instances of that policy (returning the same cached instance
// will cause problems)
type PolicyFactory func() policy.Policy

// TaskListFactory is a function to create TaskLists. The function is expected
// to return the same task list each time it's called, but it MUST return
// different instances of that task list (returning the same cached instance
// will cause problems)
type TaskListFactory func() tasklist.TaskList

// ActionFactory is a function to create Actions. The function is expected
// to return the same action each time it's called, but it MUST return
// different instances of that action (returning the same cached instance
// will cause problems)
type ActionFactory func() action.Action

// queue represents a particular task queue in the debouncer
type queue struct {
	id     string
	policy policy.Policy
	tasks  tasklist.TaskList
	action action.Action
}

// Debouncer will hold different queues, each one holding a set of tasks.
// An action (set in the queue) will run on those tasks when the policy
// (also set in the queue) triggers.
// A default policy, action and task list can be set. Those will be used
// if no policy, action or task list is provided when a new queue is created.
type Debouncer struct {
	queues                 *sync.Map
	defaultPolicyFactory   PolicyFactory
	defaultTaskListFactory TaskListFactory
	defaultActionFactory   ActionFactory
}

// NewDebouncer will create a new instance of the debouncer, using the
// provided factories in the options.
func NewDebouncer(opts ...Option) *Debouncer {
	// default options
	options := Options{
		DefaultPolicyFactory:   func() policy.Policy { return policy.NewTimedWithReset(15 * time.Second) },
		DefaultTaskListFactory: func() tasklist.TaskList { return tasklist.NewSliceWithLocks() },
		DefaultActionFactory:   func() action.Action { return action.NewChooseLast() },
	}
	// overwrite defaults
	for _, o := range opts {
		o(&options)
	}

	return &Debouncer{
		queues:                 &sync.Map{},
		defaultPolicyFactory:   options.DefaultPolicyFactory,
		defaultTaskListFactory: options.DefaultTaskListFactory,
		defaultActionFactory:   options.DefaultActionFactory,
	}
}

func (d *Debouncer) createAndReturnQueue(ctx context.Context, id string, policy policy.Policy, tasks tasklist.TaskList, action action.Action) (*queue, error) {
	q := &queue{
		id:     id,
		policy: policy,
		tasks:  tasks,
		action: action,
	}

	loadedQueue, loaded := d.queues.LoadOrStore(id, q)
	if loaded {
		// New queue wasn't added because there is already an existing queue.
		// There is nothing to do, just return the queue and a proper error.
		return loadedQueue.(*queue), ErrQueueAlreadyCreated
	}

	currentSpan := trace.SpanFromContext(ctx)
	tracer := currentSpan.TracerProvider().Tracer(TracerName)
	// spawn a goroutine to monitor the new queue and run the action when
	// the policy triggers
	go func(qq *queue, tracer trace.Tracer) {
		// wait until the policy triggers
		<-qq.policy.WaitForTrigger()

		value, loaded := d.queues.LoadAndDelete(qq.id)
		if loaded {
			// transfer the logger to the new context
			logger := zerolog.Ctx(ctx)
			queueCtx := logger.WithContext(context.Background())

			// key was present -> run through the "value" queue
			finalQueue := value.(*queue)
			finalQueue.tasks.Freeze()
			internalTasks := finalQueue.tasks.ToSlice()

			newCtx, newSpan := tracer.Start(
				queueCtx,
				"Debounce RunQueue",
				trace.WithNewRoot(),
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(d.prepareTracingData(finalQueue)...),
				trace.WithLinks(d.prepareTracingLinks(internalTasks)...),
			)
			defer newSpan.End()
			finalQueue.action.RunTasks(newCtx, internalTasks)
		}
	}(q, tracer)

	return q, nil
}

// CreateQueue will explicitly create a new queue, identified by the provided
// id, using the provided policy and action. The task list used will always be
// the default one for the debouncer.
// You can use nil as policy and action in order to use the default ones.
func (d *Debouncer) CreateQueue(ctx context.Context, id string, policy policy.Policy, action action.Action) error {
	realPolicy := d.defaultPolicyFactory()
	if policy != nil {
		realPolicy = policy
	}

	realAction := d.defaultActionFactory()
	if action != nil {
		realAction = action
	}

	_, err := d.createAndReturnQueue(ctx, id, realPolicy, d.defaultTaskListFactory(), realAction)
	return err
}

// AddToQueue will add the specified task to the queue identified with the id.
// If no queue exists with that id, a new one will be created and make it
// available.
// If the task can't be added because the task list is frozen (which means
// that the queue processing has started), a new queue will be created and
// the task will be added to the new queue. Note that this retry will happen
// only once because it's expected that the policies give enough time for the
// task to be added before triggering.
func (d *Debouncer) AddToQueue(ctx context.Context, id string, task tasklist.Task) error {
	currentSpan := trace.SpanFromContext(ctx)
	tracer := currentSpan.TracerProvider().Tracer(TracerName)
	newCtx, newSpan := tracer.Start(ctx, "Debounce AddToQueue", trace.WithSpanKind(trace.SpanKindProducer))
	defer newSpan.End()

	q, err := d.createAndReturnQueue(newCtx, id, d.defaultPolicyFactory(), d.defaultTaskListFactory(), d.defaultActionFactory())
	if err != nil && !errors.Is(err, ErrQueueAlreadyCreated) {
		// it doesn't matter if the queue has been created (returned
		// already), but we can't do anything if is different
		return err
	}

	internalTask := tasklist.NewInternalTaskFromTask(newCtx, task)

	if !q.tasks.AddToList(internalTask) {
		if q.tasks.IsFrozen() {
			// if the task isn't added because the task list is frozen,
			// we got the list right before the action runs, but the action
			// froze the task list faster than us.
			// We'll retry once to avoid failing the operation. Any trigger
			// policy that the queue could have should be tolerant enough
			// not to fail adding a task right away, otherwise return the
			// error
			q, err = d.createAndReturnQueue(newCtx, id, d.defaultPolicyFactory(), d.defaultTaskListFactory(), d.defaultActionFactory())
			if err != nil && !errors.Is(err, ErrQueueAlreadyCreated) {
				return err
			}
			if !q.tasks.AddToList(internalTask) && q.tasks.IsFrozen() {
				return ErrQueueFailedAdd
			}
		} else {
			return ErrQueueFailedAdd
		}
	}

	newSpan.SetAttributes(d.prepareTracingData(q)...)
	return nil
}

func (d *Debouncer) prepareTracingLinks(itasks []tasklist.InternalTask) []trace.Link {
	links := make([]trace.Link, len(itasks))
	for i, itask := range itasks {
		spanLink := trace.Link{
			SpanContext: itask.SpanContext,
		}
		links[i] = spanLink
	}
	return links
}

func (d *Debouncer) prepareTracingData(q *queue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("ocis.debouncer.queue.id", q.id),
		attribute.String("ocis.debouncer.queue.policy.id", q.policy.GetId()),
		attribute.String("ocis.debouncer.queue.tasklist.id", q.tasks.GetId()),
		attribute.String("ocis.debouncer.queue.action.id", q.action.GetId()),
	}

	for key, value := range q.policy.GetTracingData() {
		attrs = append(attrs, attribute.String("ocis.debouncer.queue.policy."+key, value))
	}
	for key, value := range q.tasks.GetTracingData() {
		attrs = append(attrs, attribute.String("ocis.debouncer.queue.tasklist."+key, value))
	}
	for key, value := range q.action.GetTracingData() {
		attrs = append(attrs, attribute.String("ocis.debouncer.queue.action."+key, value))
	}

	return attrs
}
