package pool

import (
	"context"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestGetClientKeepaliveParams(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		kp := GetClientKeepaliveParams()
		assert.Equal(t, _defaultKeepaliveTime, kp.Time)
		assert.Equal(t, _defaultKeepaliveTimeout, kp.Timeout)
		// an idle connection nobody uses does not need to be probed, and not
		// probing it keeps us clear of a server's ping enforcement policy
		assert.False(t, kp.PermitWithoutStream)
	})

	t.Run("overridden", func(t *testing.T) {
		t.Setenv(_clientKeepaliveTimeEnv, "30s")
		t.Setenv(_clientKeepaliveTimeoutEnv, "5s")

		kp := GetClientKeepaliveParams()
		assert.Equal(t, 30*time.Second, kp.Time)
		assert.Equal(t, 5*time.Second, kp.Timeout)
	})

	// GRPC_MAX_CONNECTION_AGE, which this replaces, fell back to infinity on a
	// parse error, so a missing unit suffix silently disabled it. A typo must
	// only ever get you the working default.
	t.Run("unparseable values fall back to the default", func(t *testing.T) {
		t.Setenv(_clientKeepaliveTimeEnv, "30")
		t.Setenv(_clientKeepaliveTimeoutEnv, "not a duration")

		kp := GetClientKeepaliveParams()
		assert.Equal(t, _defaultKeepaliveTime, kp.Time)
		assert.Equal(t, _defaultKeepaliveTimeout, kp.Timeout)
	})

	t.Run("negative values fall back to the default", func(t *testing.T) {
		t.Setenv(_clientKeepaliveTimeEnv, "-1s")

		assert.Equal(t, _defaultKeepaliveTime, GetClientKeepaliveParams().Time)
	})

	// the deliberate escape hatch: grpc raises any interval below 10s to 10s,
	// so 0 cannot mean "off" - it has to be spelled as an infinite interval
	t.Run("zero disables the pings", func(t *testing.T) {
		t.Setenv(_clientKeepaliveTimeEnv, "0")

		assert.Equal(t, time.Duration(math.MaxInt64), GetClientKeepaliveParams().Time)
	})
}

// A peer that stops answering on an established connection - the node
// black-hole that wedged school-0336, school-0377 and school-0134 - must fail
// the RPCs on that connection instead of parking them forever. Nothing in the
// gRPC stack notices this on its own: the connection is up, the TCP writes
// succeed, and the answer simply never comes.
func TestClientKeepaliveDetectsABlackHoledPeer(t *testing.T) {
	if testing.Short() {
		t.Skip("grpc clamps the keepalive interval to 10s, so this test needs ~12s")
	}

	// 10s is the floor grpc enforces (internal.KeepaliveMinPingTime)
	t.Setenv(_clientKeepaliveTimeEnv, "10s")
	t.Setenv(_clientKeepaliveTimeoutEnv, "1s")

	backend := startHealthServer(t)
	proxy := startBlackHoleProxy(t, backend)

	conn, err := NewConn(proxy.addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := healthpb.NewHealthClient(conn)

	// establish the connection while the path is still healthy
	warmupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.Check(warmupCtx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "the health server should be reachable through the proxy")

	proxy.blackHole()

	// deliberately no deadline on the call: the only thing that can make it
	// return is the keepalive giving up on the connection
	start := time.Now()
	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	elapsed := time.Since(start)

	require.Error(t, err, "the call must not succeed, the peer never answered")
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Less(t, elapsed, 25*time.Second, "took much longer than the keepalive time plus timeout")
}

// startHealthServer runs a stock grpc health server and returns its address.
func startHealthServer(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, health.NewServer())
	go func() {
		_ = s.Serve(lis)
	}()
	t.Cleanup(s.Stop)

	return lis.Addr().String()
}

// blackHoleProxy forwards tcp between a client and a backend until blackHole
// is called, after which it keeps both sockets open and reads from them but
// forwards nothing in either direction. A closed socket would be reported to
// the client right away; this is the failure mode that is not.
type blackHoleProxy struct {
	lis     net.Listener
	backend string
	blocked atomic.Bool

	mu    sync.Mutex
	conns []net.Conn
}

func startBlackHoleProxy(t *testing.T, backend string) *blackHoleProxy {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	p := &blackHoleProxy{lis: lis, backend: backend}
	go p.serve()

	t.Cleanup(func() {
		_ = lis.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})

	return p
}

func (p *blackHoleProxy) addr() string {
	return p.lis.Addr().String()
}

func (p *blackHoleProxy) blackHole() {
	p.blocked.Store(true)
}

func (p *blackHoleProxy) serve() {
	for {
		client, err := p.lis.Accept()
		if err != nil {
			return
		}

		backend, err := net.Dial("tcp", p.backend)
		if err != nil {
			_ = client.Close()
			return
		}

		p.mu.Lock()
		p.conns = append(p.conns, client, backend)
		p.mu.Unlock()

		go p.pipe(backend, client)
		go p.pipe(client, backend)
	}
}

func (p *blackHoleProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.blocked.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
