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

package rhttp

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opencensus.io/plugin/ochttp"
)

func TestGetHTTPClientTransportSettings(t *testing.T) {
	if _, ok := GetHTTPClient().Transport.(*ochttp.Transport); !ok {
		t.Fatal("the transport must be an *ochttp.Transport")
	}

	base := newTransport(newOptions())
	if base.MaxIdleConnsPerHost != maxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", base.MaxIdleConnsPerHost, maxIdleConnsPerHost)
	}
	if base.IdleConnTimeout != idleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", base.IdleConnTimeout, idleConnTimeout)
	}
	if base.IdleConnTimeout >= 60*time.Second {
		t.Fatalf("IdleConnTimeout = %v, want a value below the usual 60 s idle timeout of a load balancer", base.IdleConnTimeout)
	}

	if !newTransport(newOptions(DisableKeepAlive(true))).DisableKeepAlives {
		t.Fatal("the option DisableKeepAlive must still disable keep-alive")
	}
}

// TestGetHTTPClientReusesConnections sends two bursts of parallel requests.
// The second burst must reuse the idle connections of the first burst.
func TestGetHTTPClientReusesConnections(t *testing.T) {
	const parallel = 20

	var opened atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond) // keep the requests parallel
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	client := GetHTTPClient()

	burst := func() {
		var wg sync.WaitGroup
		for i := 0; i < parallel; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := client.Get(srv.URL)
				if err != nil {
					t.Errorf("request failed: %v", err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}()
		}
		wg.Wait()
	}

	burst()
	afterFirst := opened.Load()
	burst()
	newInSecond := opened.Load() - afterFirst

	if newInSecond > 2 {
		t.Fatalf("the second burst opened %d new connections, want at most 2 (first burst: %d)", newInSecond, afterFirst)
	}
}
