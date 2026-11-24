package providercache_test

import (
	"context"
	"testing"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

func recordingCtx() (context.Context, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	return appctx.WithTracerProvider(context.Background(), tp), exp
}

func spansNamed(exp *tracetest.InMemoryExporter, name string) []tracetest.SpanStub {
	var found []tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == name {
			found = append(found, s)
		}
	}
	return found
}

// The sync span must show the size of the document and the end of the decode.
// The spans must use the tracer provider of the context, not the global one.
func TestSyncSpanHasTheSizeAndTheDecode(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, exp := recordingCtx()
	share := &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "storageid:spaceid:share1"}}
	writer := providercache.New(storage, 0)
	if err := writer.Add(ctx, "storageid", "spaceid", "storageid:spaceid:share1", share); err != nil {
		t.Fatal(err)
	}
	exp.Reset()

	reader := providercache.New(storage, 0)
	if _, err := reader.ListSpace(ctx, "storageid", "spaceid"); err != nil {
		t.Fatal(err)
	}

	var syncs []tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == "syncWithLock" {
			syncs = append(syncs, s)
		}
	}
	if len(syncs) != 1 {
		t.Fatalf("got %d syncWithLock spans, want 1", len(syncs))
	}
	var size int64 = -1
	for _, kv := range syncs[0].Attributes {
		if kv.Key == "metadata.bytes" {
			size = kv.Value.AsInt64()
		}
	}
	if size <= 0 {
		t.Errorf("metadata.bytes = %d, want the size of the document", size)
	}
	decoded := false
	for _, ev := range syncs[0].Events {
		if ev.Name == "decoded" {
			decoded = true
		}
	}
	if !decoded {
		t.Error("the span has no decoded event")
	}
}

// The wait for the space lock must be a "Grab lock" span with the space ID.
// It is a child of the method span, and the work after the wait is not its
// child.
func TestLockSpanIsAChildOfTheMethodSpan(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, exp := recordingCtx()
	c := providercache.New(storage, 0)
	if _, err := c.ListSpace(ctx, "storageid", "spaceid"); err != nil {
		t.Fatal(err)
	}
	assertLockSpan(t, exp, "ListSpace", "syncWithLock", "cs3.spaceid", "spaceid")
}

func assertLockSpan(t *testing.T, exp *tracetest.InMemoryExporter, method, work, key, value string) {
	t.Helper()
	methods, locks, works := spansNamed(exp, method), spansNamed(exp, "Grab lock"), spansNamed(exp, work)
	if len(methods) != 1 || len(locks) != 1 || len(works) == 0 {
		t.Fatalf("got %d %s, %d Grab lock and %d %s spans", len(methods), method, len(locks), len(works), work)
	}
	if locks[0].Parent.SpanID() != methods[0].SpanContext.SpanID() {
		t.Error("the Grab lock span must be a child of the method span")
	}
	for _, w := range works {
		if w.Parent.SpanID() == locks[0].SpanContext.SpanID() {
			t.Errorf("the %s span must not be a child of the Grab lock span", work)
		}
	}
	found := false
	for _, kv := range locks[0].Attributes {
		if string(kv.Key) == key && kv.Value.AsString() == value {
			found = true
		}
	}
	if !found {
		t.Errorf("the Grab lock span must have %s=%s", key, value)
	}
}
