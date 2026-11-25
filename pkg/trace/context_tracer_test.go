package trace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recordingProvider() (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	return sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)), exp
}

func spanNames(exp *tracetest.InMemoryExporter) []string {
	spans := exp.GetSpans()
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name)
	}
	return names
}

// In the single binary, each service sets the global provider at its start,
// and the last one wins. A span of a package-level tracer must therefore use
// the provider of its context, not the global provider.
func TestContextTracerUsesTheProviderOfTheContext(t *testing.T) {
	global, globalExp := recordingProvider()
	otel.SetTracerProvider(global)
	own, ownExp := recordingProvider()

	_, span := NewContextTracer("test").Start(ContextSetTracerProvider(context.Background(), own), "span")
	span.End()

	if got := spanNames(ownExp); len(got) != 1 {
		t.Errorf("the provider of the context got %v, want one span", got)
	}
	if got := spanNames(globalExp); len(got) != 0 {
		t.Errorf("the global provider got %v, want no span", got)
	}
}

// Code in a goroutine of its own often has no provider in its context, but a
// parent span. The child span must use the provider of the parent span.
func TestContextTracerUsesTheProviderOfTheParentSpan(t *testing.T) {
	global, globalExp := recordingProvider()
	otel.SetTracerProvider(global)
	parentProvider, parentExp := recordingProvider()

	ctx, parent := parentProvider.Tracer("test").Start(context.Background(), "parent")
	_, child := NewContextTracer("test").Start(ctx, "child")
	child.End()
	parent.End()

	if got := spanNames(parentExp); len(got) != 2 {
		t.Errorf("the provider of the parent span got %v, want the parent and the child", got)
	}
	if got := spanNames(globalExp); len(got) != 0 {
		t.Errorf("the global provider got %v, want no span", got)
	}
}

func TestContextTracerFallsBackToTheGlobalProvider(t *testing.T) {
	global, globalExp := recordingProvider()
	otel.SetTracerProvider(global)

	_, span := NewContextTracer("test").Start(context.Background(), "span")
	span.End()

	if got := spanNames(globalExp); len(got) != 1 {
		t.Errorf("the global provider got %v, want one span", got)
	}
}

// The gRPC instrumentation takes a provider and asks it for a tracer one time.
// ContextTracerProvider gives it a tracer that still resolves each span.
func TestContextTracerProviderResolvesEachSpan(t *testing.T) {
	global, _ := recordingProvider()
	otel.SetTracerProvider(global)
	a, aExp := recordingProvider()
	b, bExp := recordingProvider()

	tracer := ContextTracerProvider().Tracer("test")
	_, sa := tracer.Start(ContextSetTracerProvider(context.Background(), a), "a")
	sa.End()
	_, sb := tracer.Start(ContextSetTracerProvider(context.Background(), b), "b")
	sb.End()

	if got := spanNames(aExp); len(got) != 1 || got[0] != "a" {
		t.Errorf("provider a got %v, want [a]", got)
	}
	if got := spanNames(bExp); len(got) != 1 || got[0] != "b" {
		t.Errorf("provider b got %v, want [b]", got)
	}
}

// No package of reva may use a tracer of the global provider. Use
// NewContextTracer instead.
func TestNoPackageUsesTheGlobalTracer(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"pkg", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "otel.Tracer(") {
				t.Errorf("%s uses otel.Tracer. Use trace.NewContextTracer", path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}
