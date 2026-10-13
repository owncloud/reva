package debouncer

// Option defines a single option function
type Option func(o *Options)

// Options represents the available options for the debouncer
type Options struct {
	DefaultPolicyFactory   PolicyFactory
	DefaultTaskListFactory TaskListFactory
	DefaultActionFactory   ActionFactory
}

// WithDefaultPolicy provides an option to set the default policy
func WithDefaultPolicy(p PolicyFactory) Option {
	return func(o *Options) {
		o.DefaultPolicyFactory = p
	}
}

// WithDefaultAction provides an option to set the default action
func WithDefaultAction(a ActionFactory) Option {
	return func(o *Options) {
		o.DefaultActionFactory = a
	}
}

// WithDefaultTaskList provides an option to set the default task list
func WithDefaultTaskList(t TaskListFactory) Option {
	return func(o *Options) {
		o.DefaultTaskListFactory = t
	}
}
