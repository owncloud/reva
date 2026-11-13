// Copyright 2018-2026 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package rgrpc_test

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/rgrpc"
	// The auth interceptor needs the jwt token manager.
	_ "github.com/owncloud/reva/v2/pkg/token/manager/jwt"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
)

// testService is a service that does nothing. It counts the calls to Close.
type testService struct {
	closed *atomic.Int32
}

func (t testService) Register(_ *grpc.Server)        {}
func (t testService) UnprotectedEndpoints() []string { return nil }
func (t testService) Close() error {
	t.closed.Add(1)
	return nil
}

// blockingService registers a service. Its constructor blocks until the test closes the release channel.
// This simulates a slow constructor, for example a NATS connection that retries.
func blockingService(t *testing.T, name string) (entered, release chan struct{}, closed *atomic.Int32) {
	t.Helper()
	entered = make(chan struct{})
	release = make(chan struct{})
	closed = &atomic.Int32{}
	rgrpc.Register(name, func(_ map[string]interface{}, _ *grpc.Server, _ *zerolog.Logger) (rgrpc.Service, error) {
		close(entered)
		<-release
		return testService{closed: closed}, nil
	})
	t.Cleanup(func() { delete(rgrpc.Services, name) })
	return entered, release, closed
}

func newServer(t *testing.T, services ...string) *rgrpc.Server {
	t.Helper()
	svcs := map[string]interface{}{}
	for _, s := range services {
		svcs[s] = map[string]interface{}{}
	}
	s, err := rgrpc.NewServer(map[string]interface{}{
		"network":  "tcp",
		"address":  "127.0.0.1:0",
		"services": svcs,
		"interceptors": map[string]interface{}{
			"auth": map[string]interface{}{
				"token_managers": map[string]interface{}{
					"jwt": map[string]interface{}{"secret": "rgrpc-test-secret"},
				},
			},
		},
	}, zerolog.Nop(), noop.NewTracerProvider())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func startAsync(s *rgrpc.Server, ln net.Listener) chan error {
	done := make(chan error, 1)
	go func() { done <- s.Start(ln) }()
	return done
}

func waitFor(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return")
		return nil
	}
}

func TestStopBeforeStart(t *testing.T) {
	s := newServer(t)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestGracefulStopBeforeStart(t *testing.T) {
	s := newServer(t)
	if err := s.GracefulStop(); err != nil {
		t.Fatalf("GracefulStop: %v", err)
	}
}

func TestStartAfterStopDoesNotServe(t *testing.T) {
	s := newServer(t)
	if err := s.GracefulStop(); err != nil {
		t.Fatalf("GracefulStop: %v", err)
	}
	if err := waitFor(t, startAsync(s, listen(t))); err != nil {
		t.Fatalf("Start after stop: %v", err)
	}
}

// The sharing service panicked when the pod got SIGTERM during a slow service constructor.
func TestGracefulStopWhileServicesRegister(t *testing.T) {
	entered, release, closed := blockingService(t, "rgrpc-test-blocking-graceful")
	s := newServer(t, "rgrpc-test-blocking-graceful")

	done := startAsync(s, listen(t))
	<-entered

	if err := s.GracefulStop(); err != nil {
		t.Fatalf("GracefulStop: %v", err)
	}

	close(release)
	if err := waitFor(t, done); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("service closed %d times, want 1", got)
	}
}

func TestStopWhileServicesRegister(t *testing.T) {
	entered, release, closed := blockingService(t, "rgrpc-test-blocking-hard")
	s := newServer(t, "rgrpc-test-blocking-hard")

	done := startAsync(s, listen(t))
	<-entered

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	close(release)
	if err := waitFor(t, done); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("service closed %d times, want 1", got)
	}
}

func TestGracefulStopAfterStart(t *testing.T) {
	entered, release, closed := blockingService(t, "rgrpc-test-normal")
	close(release)
	s := newServer(t, "rgrpc-test-normal")

	done := startAsync(s, listen(t))
	<-entered
	// Give Start time to reach Serve. The test must pass for each order of Start and GracefulStop.
	time.Sleep(50 * time.Millisecond)

	if err := s.GracefulStop(); err != nil {
		t.Fatalf("GracefulStop: %v", err)
	}
	if err := waitFor(t, done); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("service closed %d times, want 1", got)
	}
}
