package metadata

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
)

// authGateway counts the Authenticate calls and mints a token for each call.
type authGateway struct {
	gateway.UnimplementedGatewayAPIServer
	calls atomic.Int32
	// mint returns the token of the n-th call (n starts at 1).
	mint func(n int) string
}

func (g *authGateway) Authenticate(context.Context, *gateway.AuthenticateRequest) (*gateway.AuthenticateResponse, error) {
	n := int(g.calls.Add(1))
	return &gateway.AuthenticateResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}, Token: g.mint(n)}, nil
}

// authProvider records the token of each call. It rejects the tokens that the
// test marks as revoked.
type authProvider struct {
	provider.UnimplementedProviderAPIServer
	endpoint string
	// hook runs at the start of InitiateFileDownload.
	hook func(ctx context.Context) error

	mu      sync.Mutex
	tokens  []string
	revoked map[string]bool
}

func (p *authProvider) InitiateFileDownload(ctx context.Context, _ *provider.InitiateFileDownloadRequest) (*provider.InitiateFileDownloadResponse, error) {
	md, _ := grpcmetadata.FromIncomingContext(ctx)
	tkn := ""
	if v := md.Get(ctxpkg.TokenHeader); len(v) > 0 {
		tkn = v[0]
	}
	p.mu.Lock()
	p.tokens = append(p.tokens, tkn)
	revoked := p.revoked[tkn]
	p.mu.Unlock()
	if revoked {
		return nil, status.Error(codes.Unauthenticated, "token revoked")
	}
	if p.hook != nil {
		if err := p.hook(ctx); err != nil {
			return nil, err
		}
	}
	return &provider.InitiateFileDownloadResponse{
		Status:    &rpc.Status{Code: rpc.Code_CODE_OK},
		Protocols: []*provider.FileDownloadProtocol{{Protocol: "spaces", DownloadEndpoint: p.endpoint}},
	}, nil
}

func (p *authProvider) InitiateFileUpload(context.Context, *provider.InitiateFileUploadRequest) (*provider.InitiateFileUploadResponse, error) {
	return &provider.InitiateFileUploadResponse{
		Status:    &rpc.Status{Code: rpc.Code_CODE_OK},
		Protocols: []*provider.FileUploadProtocol{{Protocol: "simple", UploadEndpoint: p.endpoint}},
	}, nil
}

func (p *authProvider) seenTokens() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.tokens...)
}

func (p *authProvider) revoke(tkn string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.revoked == nil {
		p.revoked = map[string]bool{}
	}
	p.revoked[tkn] = true
}

type authRig struct {
	cs3      *CS3
	gw       *authGateway
	prov     *authProvider
	dataGets atomic.Int32
	// dataHook runs at the start of the HTTP GET. It returns the status to send, or 0.
	dataHook func(r *http.Request) int
	now      atomic.Int64 // unix nanoseconds of the fake clock
}

func (r *authRig) clock() time.Time { return time.Unix(0, r.now.Load()) }

func (r *authRig) advance(d time.Duration) { r.now.Add(int64(d)) }

// signToken returns a JWT of the given lifetime that starts at the fake clock.
func (r *authRig) signToken(t *testing.T, id int, lifetime time.Duration) string {
	t.Helper()
	claims := jwt.MapClaims{
		"id":  strconv.Itoa(id),
		"iat": r.clock().Unix(),
		"exp": r.clock().Add(lifetime).Unix(),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("k"))
	require.NoError(t, err)
	return s
}

func newAuthRig(t *testing.T, mint func(r *authRig, n int) string) *authRig {
	t.Helper()
	r := &authRig{}
	r.now.Store(time.Now().UnixNano())

	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.dataGets.Add(1)
		if r.dataHook != nil {
			if st := r.dataHook(req); st != 0 {
				w.WriteHeader(st)
				return
			}
		}
		w.Header().Set("Etag", "etag")
		w.Header().Set("Last-Modified", time.Now().Format(time.RFC1123Z))
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(data.Close)

	r.gw = &authGateway{mint: func(n int) string { return mint(r, n) }}
	r.prov = &authProvider{endpoint: data.URL}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	gateway.RegisterGatewayAPIServer(s, r.gw)
	provider.RegisterProviderAPIServer(s, r.prov)
	go func() { _ = s.Serve(lis) }()
	addr := lis.Addr().String()
	t.Cleanup(func() {
		s.Stop()
		pool.RemoveSelector("GatewaySelector" + addr)
		pool.RemoveSelector("StorageProviderSelector" + addr)
	})

	r.cs3 = NewCS3(addr, addr)
	r.cs3.useSystemUser = true
	r.cs3.machineAuthAPIKey = "secret"
	r.cs3.serviceUser = &user.User{Id: &user.UserId{OpaqueId: "service-user"}}
	r.cs3.now = r.clock
	return r
}

func longLivedTokens(t *testing.T) func(*authRig, int) string {
	return func(r *authRig, n int) string { return r.signToken(t, n, 24*time.Hour) }
}

func TestTokenIsReusedAcrossOperations(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))

	for i := 0; i < 3; i++ {
		_, err := r.cs3.Download(context.Background(), DownloadRequest{Path: "/f"})
		require.NoError(t, err)
	}

	assert.EqualValues(t, 1, r.gw.calls.Load(), "the machine token must be cached")
	assert.Len(t, r.prov.seenTokens(), 3)
}

func TestParallelFirstCallsShareOneAuthenticate(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.cs3.Download(context.Background(), DownloadRequest{Path: "/f"})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, r.gw.calls.Load())
}

func TestTokenWithoutExpiryIsNotCached(t *testing.T) {
	r := newAuthRig(t, func(_ *authRig, n int) string { return "opaque-" + strconv.Itoa(n) })

	for i := 0; i < 2; i++ {
		_, err := r.cs3.Download(context.Background(), DownloadRequest{Path: "/f"})
		require.NoError(t, err)
	}

	assert.EqualValues(t, 2, r.gw.calls.Load(), "a token with an unknown lifetime must not be reused")
}

func TestTokenIsRefreshedAtEightyPercentOfItsLifetime(t *testing.T) {
	r := newAuthRig(t, func(r *authRig, n int) string { return r.signToken(t, n, 100*time.Second) })
	ctx := context.Background()

	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})
	require.NoError(t, err)

	r.advance(79 * time.Second)
	_, err = r.cs3.Download(ctx, DownloadRequest{Path: "/f"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.gw.calls.Load(), "at 79 percent the token is still in use")

	r.advance(2 * time.Second)
	_, err = r.cs3.Download(ctx, DownloadRequest{Path: "/f"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, r.gw.calls.Load(), "at 81 percent the token must be replaced")
}

func TestUnauthenticatedGetsOneNewTokenAndOneRetry(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	ctx := context.Background()

	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})
	require.NoError(t, err)
	// The server revokes the cached token, for example after a key rotation.
	r.prov.revoke(r.prov.seenTokens()[0])

	_, err = r.cs3.Download(ctx, DownloadRequest{Path: "/f"})
	require.NoError(t, err)

	assert.EqualValues(t, 2, r.gw.calls.Load(), "one new token")
	assert.Len(t, r.prov.seenTokens(), 3, "first call, rejected call, retry")
}

func TestUnauthenticatedIsRetriedOnlyOnce(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	r.prov.hook = nil
	// Every token is rejected.
	r.gw.mint = func(n int) string {
		tkn := r.signToken(t, n, 24*time.Hour)
		r.prov.revoke(tkn)
		return tkn
	}

	_, err := r.cs3.Download(context.Background(), DownloadRequest{Path: "/f"})

	require.Error(t, err)
	assert.EqualValues(t, 2, r.gw.calls.Load(), "the first token and one new token")
	assert.Len(t, r.prov.seenTokens(), 2, "the call and one retry, no more")
}

func TestHTTPUnauthorizedGetsOneNewTokenAndOneRetry(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	var gets atomic.Int32
	r.dataHook = func(*http.Request) int {
		if gets.Add(1) == 1 {
			return http.StatusUnauthorized
		}
		return 0
	}

	_, err := r.cs3.Download(context.Background(), DownloadRequest{Path: "/f"})

	require.NoError(t, err)
	assert.EqualValues(t, 2, r.gw.calls.Load())
	assert.EqualValues(t, 2, r.dataGets.Load())
}

func TestTheTokenOfTheCallerIsNotSentToTheStorage(t *testing.T) {
	r := newAuthRig(t, func(_ *authRig, n int) string { return "machine-token" })
	ctx := grpcmetadata.AppendToOutgoingContext(context.Background(), ctxpkg.TokenHeader, "caller-token")

	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})

	require.NoError(t, err)
	assert.Equal(t, []string{"machine-token"}, r.prov.seenTokens())
}

// The deadline of the caller must reach InitiateFileDownload.
func TestDeadlineOfTheCallerReachesTheGRPCCall(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	r.prov.hook = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})

	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}

// The deadline of the caller must reach the HTTP GET.
func TestDeadlineOfTheCallerReachesTheHTTPCall(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	r.dataHook = func(req *http.Request) int {
		select {
		case <-release:
		case <-req.Context().Done():
		}
		return http.StatusInternalServerError
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})

	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestUploadHonorsTheCancelOfTheCaller(t *testing.T) {
	r := newAuthRig(t, longLivedTokens(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := r.cs3.Upload(ctx, UploadRequest{Path: "/f", Content: []byte("{}")})

	require.Error(t, err)
}

// A stuck Authenticate of one caller must not block the other callers
// forever. A waiter leaves when its own context ends.
func TestWaiterOfASharedAuthenticateLeavesWithItsOwnContext(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r := newAuthRig(t, func(r *authRig, n int) string {
		<-block
		return r.signToken(t, n, time.Hour)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := r.cs3.Download(ctx, DownloadRequest{Path: "/f"})

	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second)
}
