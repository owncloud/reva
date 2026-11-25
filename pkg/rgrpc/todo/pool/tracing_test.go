package pool

import (
	"context"
	"net"
	"testing"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"

	rtrace "github.com/owncloud/reva/v2/pkg/trace"
)

type statProvider struct {
	provider.UnimplementedProviderAPIServer
}

func (statProvider) Stat(context.Context, *provider.StatRequest) (*provider.StatResponse, error) {
	return &provider.StatResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil
}

// One connection of the pool serves the calls of all services in the single
// binary. Its client spans must use the tracer provider of the context of
// each call, not the global provider of the time when the pool dialed.
func TestClientSpansUseTheProviderOfTheContext(t *testing.T) {
	globalExp := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(globalExp)))
	ownExp := tracetest.NewInMemoryExporter()
	ctx := rtrace.ContextSetTracerProvider(context.Background(), sdktrace.NewTracerProvider(sdktrace.WithSyncer(ownExp)))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	provider.RegisterProviderAPIServer(s, statProvider{})
	go func() { _ = s.Serve(lis) }()
	addr := lis.Addr().String()
	t.Cleanup(func() {
		s.Stop()
		RemoveSelector("StorageProviderSelector" + addr)
	})

	client, err := GetStorageProviderServiceClient(addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stat(ctx, &provider.StatRequest{}); err != nil {
		t.Fatal(err)
	}

	if len(ownExp.GetSpans()) != 1 {
		t.Errorf("the provider of the context got %d client spans, want 1", len(ownExp.GetSpans()))
	}
	if len(globalExp.GetSpans()) != 0 {
		t.Errorf("the global provider got %d client spans, want 0", len(globalExp.GetSpans()))
	}
}
