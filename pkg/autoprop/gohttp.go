package autoprop

import (
	"context"
	"net/http"
)

func NewHttpHandler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := moveHttpHeadersToOcisMeta(r, r.Context())
			next(w, r.WithContext(ctx))
		})
	}
}

type autoPropRoundTripper struct {
	base http.RoundTripper
}

func (rt *autoPropRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	moveOcisMetaToHttpHeaders(r, r.Context())
	return rt.base.RoundTrip(r)
}

func NewHttpRoundTripper(base http.RoundTripper) http.RoundTripper {
	return &autoPropRoundTripper{
		base: base,
	}
}

func AppendToHttpRequest(r *http.Request, ctx context.Context) {
	moveOcisMetaToHttpHeaders(r, ctx)
}
