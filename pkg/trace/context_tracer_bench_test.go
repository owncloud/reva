package trace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The decomposedfs packages start hundreds of spans for one request. These
// benchmarks compare the cost of one span with the global tracer and with
// the context tracer, on the same provider.
//
// NeverSample is the provider of a service with tracing off. The span is
// created and dropped. AlwaysSample records each span, with no exporter.
func BenchmarkSpanStart(b *testing.B) {
	for _, sampler := range []struct {
		name string
		s    sdktrace.Sampler
	}{
		{"never-sample", sdktrace.NeverSample()},
		{"always-sample", sdktrace.AlwaysSample()},
	} {
		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler.s))
		otel.SetTracerProvider(tp)
		withProvider := ContextSetTracerProvider(context.Background(), tp)

		b.Run(sampler.name+"/global-tracer", func(b *testing.B) {
			tracer := otel.Tracer("bench")
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, span := tracer.Start(withProvider, "span")
					span.End()
				}
			})
		})
		b.Run(sampler.name+"/context-tracer", func(b *testing.B) {
			tracer := NewContextTracer("bench")
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, span := tracer.Start(withProvider, "span")
					span.End()
				}
			})
		})
		b.Run(sampler.name+"/context-tracer-fallback", func(b *testing.B) {
			tracer := NewContextTracer("bench")
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, span := tracer.Start(context.Background(), "span")
					span.End()
				}
			})
		})
	}
}
