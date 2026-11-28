package metadata

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/owncloud/reva/v2/pkg/appctx"
)

var (
	spanExporterOnce sync.Once
	spanExporter     *tracetest.InMemoryExporter
)

// recordSpans makes the global tracer record into an in-memory exporter. The
// global provider can be set only one time, so all tests share the exporter.
// The tests that use it must not run in parallel.
func recordSpans(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	spanExporterOnce.Do(func() {
		spanExporter = tracetest.NewInMemoryExporter()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spanExporter)))
	})
	spanExporter.Reset()
	return spanExporter
}

// spanAttributes returns the attributes of the only span with the name.
func spanAttributes(t *testing.T, exp *tracetest.InMemoryExporter, name string) (map[attribute.Key]attribute.Value, sdktrace.Status) {
	t.Helper()
	var found []tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == name {
			found = append(found, s)
		}
	}
	require.Len(t, found, 1, "want one span %q", name)
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range found[0].Attributes {
		attrs[kv.Key] = kv.Value
	}
	return attrs, found[0].Status
}

// A trace must show the result of each metadata call. Otherwise it cannot
// tell a 200 from a 304 or a 404, or an upload from a CAS conflict.
func TestDownloadSpanHasTheResult(t *testing.T) {
	for name, tc := range map[string]struct {
		ifNoneMatch []string
		getStatus   int32
		wantResult  string
		wantStatus  int64
		wantBytes   int64
	}{
		"200": {wantResult: "200", wantStatus: http.StatusOK, wantBytes: 2},
		"304": {ifNoneMatch: []string{"etag"}, wantResult: "304", wantStatus: http.StatusNotModified},
		"404": {getStatus: http.StatusNotFound, wantResult: "404", wantStatus: http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			exp := recordSpans(t)
			cs3, _, fd := newTracedCS3WithData(t)
			fd.getStatus.Store(tc.getStatus)

			_, _ = cs3.Download(context.Background(), DownloadRequest{Path: "/users/u/received.json", IfNoneMatch: tc.ifNoneMatch})

			attrs, status := spanAttributes(t, exp, "Download")
			assert.Equal(t, "/users/u/received.json", attrs["metadata.path"].AsString())
			assert.Equal(t, tc.wantResult, attrs["metadata.result"].AsString())
			assert.Equal(t, tc.wantStatus, attrs["http.response.status_code"].AsInt64())
			assert.Equal(t, tc.wantBytes, attrs["metadata.bytes"].AsInt64())
			assert.NotEqual(t, codes.Error, status.Code, "an expected result is not a span error")
		})
	}
}

func TestUploadSpanHasTheResult(t *testing.T) {
	for name, tc := range map[string]struct {
		req           UploadRequest
		putStatus     int32
		wantResult    string
		wantCondition string
		wantConflict  bool
	}{
		"created": {
			req:        UploadRequest{Path: "/doc.json", Content: []byte("{}"), IfNoneMatch: []string{"*"}},
			wantResult: "200", wantCondition: "if-none-match",
		},
		"conflict": {
			req:        UploadRequest{Path: "/doc.json", Content: []byte("{}"), IfMatchEtag: "old"},
			putStatus:  http.StatusPreconditionFailed,
			wantResult: "conflict", wantCondition: "if-match", wantConflict: true,
		},
		"no condition": {
			req:        UploadRequest{Path: "/doc.json", Content: []byte("{}")},
			wantResult: "200", wantCondition: "none",
		},
	} {
		t.Run(name, func(t *testing.T) {
			exp := recordSpans(t)
			cs3, _, fd := newTracedCS3WithData(t)
			fd.putStatus.Store(tc.putStatus)

			_, _ = cs3.Upload(context.Background(), tc.req)

			attrs, status := spanAttributes(t, exp, "Upload")
			assert.Equal(t, "/doc.json", attrs["metadata.path"].AsString())
			assert.Equal(t, tc.wantResult, attrs["metadata.result"].AsString())
			assert.Equal(t, tc.wantCondition, attrs["metadata.condition"].AsString())
			assert.Equal(t, tc.wantConflict, attrs["metadata.conflict"].AsBool())
			assert.Equal(t, int64(2), attrs["metadata.bytes"].AsInt64())
			assert.NotEqual(t, codes.Error, status.Code, "a CAS conflict is an expected result, not a span error")
		})
	}
}

// Other errors are span errors.
func TestDownloadSpanRecordsAnError(t *testing.T) {
	exp := recordSpans(t)
	cs3, _, fd := newTracedCS3WithData(t)
	fd.getStatus.Store(http.StatusInternalServerError)

	_, err := cs3.Download(context.Background(), DownloadRequest{Path: "/doc.json"})
	require.Error(t, err)

	attrs, status := spanAttributes(t, exp, "Download")
	assert.Equal(t, "error", attrs["metadata.result"].AsString())
	assert.Equal(t, int64(http.StatusInternalServerError), attrs["http.response.status_code"].AsInt64())
	assert.Equal(t, codes.Error, status.Code)
}

// In the single binary, each service sets the global tracer provider at its
// start, and the last one wins. A span from the global provider then has the
// service name of a random service, or it is dropped when that service has
// tracing off. The spans must use the provider of the request context.
func TestSpansUseTheTracerProviderOfTheContext(t *testing.T) {
	global := recordSpans(t)
	own := tracetest.NewInMemoryExporter()
	ctx := appctx.WithTracerProvider(context.Background(), sdktrace.NewTracerProvider(sdktrace.WithSyncer(own)))
	cs3, _, _ := newTracedCS3WithData(t)

	_, err := cs3.Download(ctx, DownloadRequest{Path: "/doc.json"})
	require.NoError(t, err)

	inOwn := map[string]bool{}
	for _, s := range own.GetSpans() {
		inOwn[s.Name] = true
	}
	for _, name := range []string{"Download", "getAuthContext"} {
		assert.True(t, inOwn[name], "the %s span must use the provider of the context", name)
	}
	for _, s := range global.GetSpans() {
		assert.NotContains(t, []string{"Download", "getAuthContext"}, s.Name, "the %s span must not use the global provider", s.Name)
	}
}

// The other operations must also show the path and the result.
func TestOtherOperationSpansHaveThePathAndTheResult(t *testing.T) {
	for name, tc := range map[string]struct {
		call       func(cs3 *CS3) error
		wantResult string
	}{
		"MakeDirIfNotExist": {func(cs3 *CS3) error { return cs3.MakeDirIfNotExist(context.Background(), "/users/u") }, "200"},
		"Stat": {func(cs3 *CS3) error {
			_, err := cs3.Stat(context.Background(), "/users/u")
			return err
		}, "404"},
		"Delete": {func(cs3 *CS3) error { return cs3.Delete(context.Background(), "/users/u") }, "200"},
		"ListDir": {func(cs3 *CS3) error {
			_, err := cs3.ListDir(context.Background(), "/users/u")
			return err
		}, "200"},
	} {
		t.Run(name, func(t *testing.T) {
			exp := recordSpans(t)
			cs3, _, _ := newTracedCS3WithData(t)

			_ = tc.call(cs3)

			attrs, _ := spanAttributes(t, exp, name)
			assert.Equal(t, "/users/u", attrs["metadata.path"].AsString())
			assert.Equal(t, tc.wantResult, attrs["metadata.result"].AsString())
		})
	}
}
