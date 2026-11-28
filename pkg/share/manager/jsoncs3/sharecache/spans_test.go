package sharecache_test

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/sharecache"
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

// The Sync span must show the size of the document and the end of the decode.
func TestSyncSpanHasTheSizeAndTheDecode(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := sharecache.New(storage, "users", "created.json", 0)
	if err := writer.Add(context.Background(), "user1", "storageid:spaceid:share1"); err != nil {
		t.Fatal(err)
	}

	exp := tracetest.NewInMemoryExporter()
	ctx := appctx.WithTracerProvider(context.Background(), sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	reader := sharecache.New(storage, "users", "created.json", 0)
	if _, err := reader.List(ctx, "user1"); err != nil {
		t.Fatal(err)
	}

	var syncs []tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == "Sync" {
			syncs = append(syncs, s)
		}
	}
	if len(syncs) != 1 {
		t.Fatalf("got %d Sync spans, want 1", len(syncs))
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

// The wait for the lock must be a "Grab lock" span with the user ID. It is a
// child of the method span, and the work after the wait is not its child.
func TestLockSpanIsAChildOfTheMethodSpan(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, exp := recordingCtx()
	c := sharecache.New(storage, "users", "created.json", 0)
	if err := c.Add(ctx, "user1", "storageid:spaceid:share1"); err != nil {
		t.Fatal(err)
	}

	methods, locks, works := spansNamed(exp, "Add"), spansNamed(exp, "Grab lock"), spansNamed(exp, "Sync")
	if len(methods) != 1 || len(locks) != 1 || len(works) == 0 {
		t.Fatalf("got %d Add, %d Grab lock and %d Sync spans", len(methods), len(locks), len(works))
	}
	if locks[0].Parent.SpanID() != methods[0].SpanContext.SpanID() {
		t.Error("the Grab lock span must be a child of the method span")
	}
	for _, w := range works {
		if w.Parent.SpanID() == locks[0].SpanContext.SpanID() {
			t.Error("the Sync span must not be a child of the Grab lock span")
		}
	}
	found := false
	for _, kv := range locks[0].Attributes {
		if kv.Key == "cs3.userid" && kv.Value.AsString() == "user1" {
			found = true
		}
	}
	if !found {
		t.Error("the Grab lock span must have cs3.userid=user1")
	}
}
