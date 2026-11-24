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
