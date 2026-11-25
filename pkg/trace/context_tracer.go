package trace

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// NewContextTracer returns a tracer that starts each span with the tracer
// provider of the context of the span. Use it for package-level tracers.
//
// A tracer from otel.Tracer uses the global provider. In the single binary,
// each service sets the global provider at its start, and the last one wins.
// The spans of such a tracer then have the service name of a random service,
// or they are dropped when that service has tracing off.
//
// The tracer looks for a provider in this order:
//  1. the provider in the context (the gRPC and HTTP servers of reva put the
//     provider of their service there)
//  2. the provider of a recording parent span in the context
//  3. the global provider
func NewContextTracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return &contextTracer{name: name, opts: opts}
}

type contextTracer struct {
	embedded.Tracer

	name string
	opts []trace.TracerOption
	// tracers caches one tracer for each provider. The SDK takes a lock in
	// each call of Tracer, and there are only a few providers.
	tracers sync.Map // trace.TracerProvider -> trace.Tracer
}

func (t *contextTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.tracer(providerOf(ctx)).Start(ctx, spanName, opts...)
}

func (t *contextTracer) tracer(p trace.TracerProvider) trace.Tracer {
	if tr, ok := t.tracers.Load(p); ok {
		return tr.(trace.Tracer)
	}
	tr, _ := t.tracers.LoadOrStore(p, p.Tracer(t.name, t.opts...))
	return tr.(trace.Tracer)
}

// ContextTracerProvider returns a tracer provider whose tracers resolve the
// provider of each span from its context, as NewContextTracer does. Use it
// for instrumentation that asks for a tracer only one time, for example the
// gRPC client handler of a shared connection.
func ContextTracerProvider() trace.TracerProvider {
	return contextProvider
}

var contextProvider = &contextTracerProvider{}

type contextTracerProvider struct {
	embedded.TracerProvider

	tracers sync.Map // name -> trace.Tracer
}

func (p *contextTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	if len(opts) > 0 {
		return NewContextTracer(name, opts...)
	}
	if tr, ok := p.tracers.Load(name); ok {
		return tr.(trace.Tracer)
	}
	tr, _ := p.tracers.LoadOrStore(name, NewContextTracer(name))
	return tr.(trace.Tracer)
}

// providerOf returns the provider for a new span in ctx. See NewContextTracer.
func providerOf(ctx context.Context) trace.TracerProvider {
	if p, ok := ctx.Value(ctxKey{}).(trace.TracerProvider); ok {
		return p
	}
	if parent := trace.SpanFromContext(ctx); parent.IsRecording() {
		return parent.TracerProvider()
	}
	return DefaultProvider()
}
