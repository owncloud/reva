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
