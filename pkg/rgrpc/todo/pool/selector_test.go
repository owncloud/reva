// Copyright 2018-2021 CERN
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

package pool

import (
	"testing"

	"github.com/stretchr/testify/require"
	mRegistry "go-micro.dev/v4/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// newTestConn returns a lazy, unconnected grpc.ClientConn to a fake target.
// GetState() starts at connectivity.Idle, which healthy() treats as healthy.
func newTestConn(t *testing.T) *grpc.ClientConn {
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newDeadConn returns a grpc.ClientConn whose GetState() is Shutdown, which
// healthy() treats as not healthy - standing in for a connection to a
// backend that already failed a real RPC.
func newDeadConn(t *testing.T) *grpc.ClientConn {
	conn := newTestConn(t)
	require.NoError(t, conn.Close())
	return conn
}

func newTestSelector() *Selector[string] {
	return &Selector[string]{
		id: "test-service",
		clientFactory: func(cc grpc.ClientConnInterface) string {
			return "client"
		},
	}
}

func TestShuffledTargets(t *testing.T) {
	services := []*mRegistry.Service{
		{Nodes: []*mRegistry.Node{{Address: "10.0.0.1:9157"}, {Address: "10.0.0.2:9157"}}},
		{Nodes: []*mRegistry.Node{{Address: "10.0.0.3:9157"}}},
	}

	targets := shuffledTargets(services)

	require.ElementsMatch(t, []string{"10.0.0.1:9157", "10.0.0.2:9157", "10.0.0.3:9157"}, targets)
}

func TestShuffledTargetsEmpty(t *testing.T) {
	require.Empty(t, shuffledTargets(nil))
}

func TestNextHealthySkipsDeadTarget(t *testing.T) {
	s := newTestSelector()
	s.clientMap.Store("dead:9157", cachedClient[string]{conn: newDeadConn(t), client: "dead-client"})

	// "unknown:9157" has no cache entry at all, so it is dialed fresh -
	// which succeeds immediately because grpc.NewClient is lazy.
	client, err := s.nextHealthy([]string{"dead:9157", "unknown:9157"}, nil)

	require.NoError(t, err)
	require.Equal(t, "client", client)
	require.NotEqual(t, "dead-client", client)
}

func TestNextHealthyPrefersHealthyCachedOverDead(t *testing.T) {
	s := newTestSelector()
	s.clientMap.Store("dead:9157", cachedClient[string]{conn: newDeadConn(t), client: "dead-client"})
	s.clientMap.Store("healthy:9157", cachedClient[string]{conn: newTestConn(t), client: "healthy-client"})

	client, err := s.nextHealthy([]string{"dead:9157", "healthy:9157"}, nil)

	require.NoError(t, err)
	require.Equal(t, "healthy-client", client)
}

func TestNextHealthyFallsBackWhenEveryTargetIsDead(t *testing.T) {
	s := newTestSelector()
	s.clientMap.Store("dead-1:9157", cachedClient[string]{conn: newDeadConn(t), client: "dead-client-1"})
	s.clientMap.Store("dead-2:9157", cachedClient[string]{conn: newDeadConn(t), client: "dead-client-2"})

	client, err := s.nextHealthy([]string{"dead-1:9157", "dead-2:9157"}, nil)

	require.NoError(t, err)
	require.Equal(t, "dead-client-1", client)
}

func TestLoadOrDialCachesClient(t *testing.T) {
	s := newTestSelector()

	first, err := s.loadOrNewClient("some-target:9157", nil)
	require.NoError(t, err)
	require.Equal(t, "client", first)

	cached, ok := s.clientMap.Load("some-target:9157")
	require.True(t, ok)
	require.Equal(t, "client", cached.(cachedClient[string]).client)
}
