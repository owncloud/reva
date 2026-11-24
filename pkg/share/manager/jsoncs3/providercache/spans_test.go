package providercache_test

import (
	"context"
	"testing"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// The sync span must show the size of the document and the end of the decode.
// providercache uses the global tracer. The test sets the global provider, so
// it must not run in parallel with other span tests.
func TestSyncSpanHasTheSizeAndTheDecode(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))

	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
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
