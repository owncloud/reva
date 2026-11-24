package metadata

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	grpcmetadata "google.golang.org/grpc/metadata"

	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
)

// traceRecorder keeps the trace ID that each call carried.
type traceRecorder struct {
	mu     sync.Mutex
	traces map[string]trace.TraceID
}

func (r *traceRecorder) record(call string, carrier propagation.TextMapCarrier) {
	sc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.traces[call] = sc.TraceID()
}

func (r *traceRecorder) get(call string) (trace.TraceID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.traces[call]
	return id, ok
}

// grpcCarrier reads the incoming gRPC metadata as a text map.
type grpcCarrier grpcmetadata.MD

func (c grpcCarrier) Get(key string) string {
	if v := grpcmetadata.MD(c).Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}
func (c grpcCarrier) Set(string, string) {}
func (c grpcCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

func recordGRPC(ctx context.Context, rec *traceRecorder, call string) {
	md, _ := grpcmetadata.FromIncomingContext(ctx)
	rec.record(call, grpcCarrier(md))
}

// tracedGateway answers Authenticate with a fixed token.
type tracedGateway struct {
	gateway.UnimplementedGatewayAPIServer
	rec *traceRecorder
}

func (g *tracedGateway) Authenticate(ctx context.Context, _ *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
	recordGRPC(ctx, g.rec, "Authenticate")
	return &gateway.AuthenticateResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}, Token: "token"}, nil
}

// tracedProvider returns the HTTP endpoint for downloads and uploads.
type tracedProvider struct {
	provider.UnimplementedProviderAPIServer
	rec      *traceRecorder
	endpoint string
}

func (p *tracedProvider) InitiateFileDownload(ctx context.Context, _ *provider.InitiateFileDownloadRequest) (*provider.InitiateFileDownloadResponse, error) {
	recordGRPC(ctx, p.rec, "InitiateFileDownload")
	return &provider.InitiateFileDownloadResponse{
		Status:    &rpc.Status{Code: rpc.Code_CODE_OK},
		Protocols: []*provider.FileDownloadProtocol{{Protocol: "spaces", DownloadEndpoint: p.endpoint}},
	}, nil
}

func (p *tracedProvider) InitiateFileUpload(ctx context.Context, _ *provider.InitiateFileUploadRequest) (*provider.InitiateFileUploadResponse, error) {
	recordGRPC(ctx, p.rec, "InitiateFileUpload")
	return &provider.InitiateFileUploadResponse{
		Status:    &rpc.Status{Code: rpc.Code_CODE_OK},
		Protocols: []*provider.FileUploadProtocol{{Protocol: "simple", UploadEndpoint: p.endpoint}},
	}, nil
}

// newTracedCS3 starts a fake gateway, a fake storage provider and a fake data
// server. It returns a CS3 that authenticates as the service user.
func newTracedCS3(t *testing.T) (*CS3, *traceRecorder) {
	t.Helper()
	rec := &traceRecorder{traces: map[string]trace.TraceID{}}

	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record("HTTP "+r.Method, propagation.HeaderCarrier(r.Header))
		w.Header().Set("Etag", "etag")
		w.Header().Set("Last-Modified", time.Now().Format(time.RFC1123Z))
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(data.Close)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	gateway.RegisterGatewayAPIServer(s, &tracedGateway{rec: rec})
	provider.RegisterProviderAPIServer(s, &tracedProvider{rec: rec, endpoint: data.URL})
	go func() { _ = s.Serve(lis) }()
	addr := lis.Addr().String()
	t.Cleanup(func() {
		s.Stop()
		pool.RemoveSelector("GatewaySelector" + addr)
		pool.RemoveSelector("StorageProviderSelector" + addr)
	})

	cs3 := NewCS3(addr, addr)
	cs3.useSystemUser = true
	cs3.machineAuthAPIKey = "secret"
	cs3.serviceUser = &user.User{Id: &user.UserId{OpaqueId: "service-user"}}
	return cs3, rec
}

// requestContext returns a context with the span of an incoming request.
func requestContext() (context.Context, trace.TraceID) {
	traceID := trace.TraceID{0x0a, 0x0b, 0x0c, 0x0d, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), sc), traceID
}

// Each call of a metadata operation must be in the trace of the request.
// Otherwise a trace of a request does not show the work on the metadata
// storage, and the work cannot be counted for one request.
func TestDownloadCallsStayInTheTraceOfTheRequest(t *testing.T) {
	cs3, rec := newTracedCS3(t)
	ctx, traceID := requestContext()

	_, err := cs3.Download(ctx, DownloadRequest{Path: "/some/file"})
	require.NoError(t, err)

	for _, call := range []string{"Authenticate", "InitiateFileDownload", "HTTP GET"} {
		got, ok := rec.get(call)
		require.True(t, ok, "the fake did not get the call %s", call)
		assert.Equal(t, traceID, got, "%s must be in the trace of the request", call)
	}
}

func TestUploadCallsStayInTheTraceOfTheRequest(t *testing.T) {
	cs3, rec := newTracedCS3(t)
	ctx, traceID := requestContext()

	_, err := cs3.Upload(ctx, UploadRequest{Path: "/some/file", Content: []byte("{}")})
	require.NoError(t, err)

	for _, call := range []string{"Authenticate", "InitiateFileUpload", "HTTP PUT"} {
		got, ok := rec.get(call)
		require.True(t, ok, "the fake did not get the call %s", call)
		assert.Equal(t, traceID, got, "%s must be in the trace of the request", call)
	}
}

// The trace fix must not change the lifetime of the calls. A cancel of the
// request does not stop a metadata call yet. That is a separate change.
func TestDownloadStillIgnoresTheCancelOfTheRequest(t *testing.T) {
	cs3, _ := newTracedCS3(t)
	ctx, _ := requestContext()
	ctx, cancel := context.WithCancel(ctx)
	cancel()

	_, err := cs3.Download(ctx, DownloadRequest{Path: "/some/file"})
	assert.NoError(t, err)
}
