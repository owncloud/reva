package trace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestContextGetTracerProviderOrDefault(t *testing.T) {
	global := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(global)
	own := sdktrace.NewTracerProvider()

	if got := ContextGetTracerProviderOrDefault(ContextSetTracerProvider(context.Background(), own)); got != own {
		t.Error("with a provider in the context, want that provider")
	}
	// The global provider is a delegate, so compare by the spans it makes.
	_, span := ContextGetTracerProviderOrDefault(context.Background()).Tracer("test").Start(context.Background(), "span")
	defer span.End()
	if !span.SpanContext().IsValid() {
		t.Error("without a provider in the context, want the global provider, not a no-op provider")
	}
}
