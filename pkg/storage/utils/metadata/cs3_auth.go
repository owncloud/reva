package metadata

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel/baggage"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/owncloud/reva/v2/pkg/appctx"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	rtrace "github.com/owncloud/reva/v2/pkg/trace"
)

// tokenRefreshFraction is the part of the lifetime of a machine token after
// which the storage gets a new token. The storage never uses a token that is
// close to its end.
const tokenRefreshFraction = 0.8

// tokenCache holds the machine token of one CS3 instance.
type tokenCache struct {
	mu        sync.Mutex
	token     string
	refreshAt time.Time

	// group lets parallel callers share one Authenticate call.
	group singleflight.Group
}

// get returns the cached token when it is still valid.
func (c *tokenCache) get(now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || !now.Before(c.refreshAt) {
		return "", false
	}
	return c.token, true
}

func (c *tokenCache) set(token string, refreshAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token, c.refreshAt = token, refreshAt
}

// invalidate removes the token from the cache. It keeps a newer token that
// another caller stored in the meantime.
func (c *tokenCache) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token, c.refreshAt = "", time.Time{}
	}
}

// tokenRefreshTime returns the time at which the token must be replaced. It
// reads the claims of the token and does not verify the signature: the
// storage only needs the lifetime, and the services verify the token on each
// call. It returns false when the token has no usable expiry. The storage
// does not cache such a token.
func tokenRefreshTime(token string, now time.Time) (time.Time, bool) {
	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil || claims.ExpiresAt == nil {
		return time.Time{}, false
	}
	start := now
	if claims.IssuedAt != nil && claims.IssuedAt.Before(now) {
		start = claims.IssuedAt.Time
	}
	lifetime := claims.ExpiresAt.Sub(start)
	if lifetime <= 0 {
		return time.Time{}, false
	}
	refreshAt := start.Add(time.Duration(float64(lifetime) * tokenRefreshFraction))
	if !refreshAt.After(now) {
		return time.Time{}, false
	}
	return refreshAt, true
}

func (cs3 *CS3) clock() time.Time {
	if cs3.now != nil {
		return cs3.now()
	}
	return time.Now()
}

// unauthenticatedError reports that a service did not accept the machine
// token. It is an internal error for all callers that do not know it.
type unauthenticatedError string

func (e unauthenticatedError) Error() string { return string(e) }

// IsInternalError implements the errtypes.IsInternalError interface.
func (unauthenticatedError) IsInternalError() {}

// statusToErr maps a CS3 status to an error. It marks the status
// UNAUTHENTICATED, so that the storage can get a new token.
func statusToErr(s *rpc.Status) error {
	if s.GetCode() == rpc.Code_CODE_UNAUTHENTICATED {
		return unauthenticatedError(s.GetMessage())
	}
	return errtypes.NewErrtypeFromStatus(s)
}

// httpStatusToErr maps an HTTP status to an error. It marks the status 401,
// so that the storage can get a new token.
func httpStatusToErr(code int, message string) error {
	if code == http.StatusUnauthorized {
		return unauthenticatedError(message)
	}
	return errtypes.NewErrtypeFromHTTPStatusCode(code, message)
}

func isUnauthenticated(err error) bool {
	var ue unauthenticatedError
	if errors.As(err, &ue) {
		return true
	}
	return err != nil && status.Code(err) == codes.Unauthenticated
}

// withAuth runs call with the machine token of the service user. When a
// service rejects the token, it gets a new token and runs call one more time.
// call must be safe to run again: a rejected token stops the call before it
// changes anything.
func (cs3 *CS3) withAuth(ctx context.Context, call func(ctx context.Context) error) error {
	if !cs3.useSystemUser {
		return call(ctx)
	}
	authCtx, token, err := cs3.authContext(ctx)
	if err != nil {
		return err
	}
	err = call(authCtx)
	if !isUnauthenticated(err) {
		return err
	}
	appctx.GetLogger(ctx).Debug().Msg("cs3: machine token rejected, get a new token")
	cs3.tokens.invalidate(token)
	authCtx, _, err = cs3.authContext(ctx)
	if err != nil {
		return err
	}
	return call(authCtx)
}

func (cs3 *CS3) getAuthContext(ctx context.Context) (context.Context, error) {
	authCtx, _, err := cs3.authContext(ctx)
	return authCtx, err
}

// authContext returns a context for the calls of one operation, and the
// machine token that it carries. The new context keeps the deadline, the
// cancel, the span and the baggage of the caller. It does not keep the
// outgoing metadata of the caller, so the token of the caller never reaches
// the storage.
func (cs3 *CS3) authContext(ctx context.Context) (context.Context, string, error) {
	if !cs3.useSystemUser {
		return ctx, "", nil
	}
	token, err := cs3.machineToken(ctx)
	if err != nil {
		return nil, "", err
	}
	authCtx := metadata.NewOutgoingContext(ctx, metadata.MD{})
	authCtx = ctxpkg.ContextSetUser(authCtx, cs3.serviceUser)
	authCtx = ctxpkg.ContextSetToken(authCtx, token)
	authCtx = metadata.AppendToOutgoingContext(authCtx, ctxpkg.TokenHeader, token)
	return authCtx, token, nil
}

// machineToken returns the cached token, or gets a new one. Parallel callers
// share one Authenticate call. Each caller waits with its own context. The
// shared call ends with the timeout of the Authenticate call, not with the
// cancel of one caller.
func (cs3 *CS3) machineToken(ctx context.Context) (string, error) {
	if token, ok := cs3.tokens.get(cs3.clock()); ok {
		return token, nil
	}
	ch := cs3.tokens.group.DoChan("authenticate", func() (any, error) {
		if token, ok := cs3.tokens.get(cs3.clock()); ok {
			return token, nil
		}
		return cs3.authenticate(context.WithoutCancel(ctx))
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		return res.Val.(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// authenticate gets a new machine token from the gateway and stores it.
func (cs3 *CS3) authenticate(ctx context.Context) (string, error) {
	// The new context keeps the span and the baggage of the caller, so the
	// call stays in the trace of the request. It does not keep the outgoing
	// metadata of the caller.
	authCtx := metadata.NewOutgoingContext(ctx, metadata.MD{})
	authCtx = baggage.ContextWithBaggage(authCtx, baggage.FromContext(ctx))
	authCtx = rtrace.ContextSetTracerProvider(authCtx, rtrace.ContextGetTracerProviderOrDefault(ctx))
	authCtx = ctxpkg.ContextSetUser(authCtx, cs3.serviceUser)

	// The span covers only the Authenticate call.
	spanCtx, span := startSpan(authCtx, "getAuthContext")
	defer span.End()

	selector, err := pool.GatewaySelector(cs3.gatewayAddr)
	if err != nil {
		return "", err
	}
	client, err := selector.Next()
	if err != nil {
		return "", err
	}

	// The Authenticate call gets a timeout of its own: a gateway that is stuck
	// on a downstream storage provider accepts the connection and then never
	// answers, and there is nothing else that would ever end the wait.
	timeout := cs3.authRPCTimeout
	if timeout <= 0 {
		timeout = defaultAuthRPCTimeout
	}
	rpcCtx, cancel := context.WithTimeout(spanCtx, timeout)
	defer cancel()

	start := time.Now()
	authRes, err := client.Authenticate(rpcCtx, &gateway.AuthenticateRequest{
		Type:         "machine",
		ClientId:     "userid:" + cs3.serviceUser.Id.OpaqueId,
		ClientSecret: cs3.machineAuthAPIKey,
	})
	if err != nil {
		// this used to fail silently by never returning at all, so make sure
		// an exhausted deadline is visible at production log level
		appctx.GetLogger(ctx).Warn().Err(err).Str("gateway_addr", cs3.gatewayAddr).
			Dur("elapsed", time.Since(start)).Dur("timeout", timeout).Msg("cs3: machine authentication failed")
		return "", err
	}
	if authRes.GetStatus().GetCode() != rpc.Code_CODE_OK {
		return "", errtypes.NewErrtypeFromStatus(authRes.GetStatus())
	}
	if refreshAt, ok := tokenRefreshTime(authRes.Token, cs3.clock()); ok {
		cs3.tokens.set(authRes.Token, refreshAt)
	}
	return authRes.Token, nil
}
