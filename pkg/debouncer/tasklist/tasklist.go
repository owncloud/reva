package tasklist

// TaskList represents a list of tasks. The basic idea is a []Task BUT all the
// operations MUST be thread-safe.
// Different implementations can be provided, backed by different data
// structures, as long as the implementation is thread-safe.
type TaskList interface {
	// AddToList adds the task to the list. Returns true if the task is
	// added, false if not.
	AddToList(task InternalTask) bool
	// Freeze the task list so no new task can be added. After this method
	// returns, the AddToList method MUST always return false.
	Freeze()
	// IsFrozen returns the frozen status of the task list
	IsFrozen() bool
	// ToSlice returns the tasks as a slice. It's recommended to Freeze
	// the task list implementation before this method so all the tasks
	// are present in the slice. If the implementation isn't frozen, a
	// snapshot of the list is expected (new tasks might be added to the
	// list while this method is running)
	ToSlice() []InternalTask
	// GetId returns the ID of this instance. It must be unique, so multiple
	// instances from the same task list type must return different IDs.
	// The recommendation is to use the instance type followed by a random
	// number, such as "SliceWithLocks_123987"
	GetId() string
	// GetTracingData returns additional data that will be used for tracing.
	// Consider this data as public information. This data is intended to
	// be use purely for informational purposes. You can return nil if
	// there isn't any data to be published.
	GetTracingData() map[string]string
}
