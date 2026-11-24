package receivedsharecache_test

import (
	"context"
	"testing"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/receivedsharecache"
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

// Each listing reads the user document under the user lock. The trace must
// show the wait for the lock, as the other methods of the cache do.
func TestListHasALockSpan(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := receivedsharecache.New(storage, 0)
	ctx, exp := recordingCtx()

	if _, err := c.List(ctx, "user1"); err != nil {
		t.Fatal(err)
	}

	lists := spansNamed(exp, "List")
	locks := spansNamed(exp, "Grab lock")
	if len(lists) != 1 || len(locks) != 1 {
		t.Fatalf("got %d List spans and %d Grab lock spans, want 1 and 1", len(lists), len(locks))
	}
	if locks[0].Parent.SpanID() != lists[0].SpanContext.SpanID() {
		t.Fatal("the Grab lock span must be a child of the List span")
	}
}

// The Sync span must show the size of the document and the end of the decode.
func TestSyncSpanHasTheSizeAndTheDecode(t *testing.T) {
	storage, err := metadata.NewDiskStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rs := &collaboration.ReceivedShare{
		Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "storageid:spaceid:share1"}},
		State: collaboration.ShareState_SHARE_STATE_PENDING,
	}
	writer := receivedsharecache.New(storage, 0)
	if err := writer.Add(context.Background(), "user1", "storageid:spaceid", rs); err != nil {
		t.Fatal(err)
	}

	ctx, exp := recordingCtx()
	reader := receivedsharecache.New(storage, 0)
	if _, err := reader.List(ctx, "user1"); err != nil {
		t.Fatal(err)
	}
	syncs := spansNamed(exp, "Sync")
	if len(syncs) != 1 {
		t.Fatalf("got %d Sync spans, want 1", len(syncs))
	}
	assertSizeAndDecode(t, syncs[0])
}

func assertSizeAndDecode(t *testing.T, s tracetest.SpanStub) {
	t.Helper()
	var size int64 = -1
	for _, kv := range s.Attributes {
		if kv.Key == "metadata.bytes" {
			size = kv.Value.AsInt64()
		}
	}
	if size <= 0 {
		t.Errorf("metadata.bytes = %d, want the size of the document", size)
	}
	decoded := false
	for _, ev := range s.Events {
		if ev.Name == "decoded" {
			decoded = true
		}
	}
	if !decoded {
		t.Error("the span has no decoded event")
	}
}
