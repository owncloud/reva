// Copyright 2018-2022 CERN
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

package metadata

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewCS3UsesTheSharedDataGatewayTransport(t *testing.T) {
	a := NewCS3("gateway", "provider")
	b := NewCS3("gateway", "provider")

	if a.dataGatewayClient == http.DefaultClient {
		t.Fatal("the data gateway client must not be http.DefaultClient")
	}
	if a.dataGatewayClient.Transport != dataGatewayTransport || b.dataGatewayClient.Transport != dataGatewayTransport {
		t.Fatal("all CS3 instances must share one transport")
	}
	if dataGatewayTransport.MaxIdleConnsPerHost != dataGatewayMaxIdleConns {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", dataGatewayTransport.MaxIdleConnsPerHost, dataGatewayMaxIdleConns)
	}
	if dataGatewayTransport.IdleConnTimeout != dataGatewayIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", dataGatewayTransport.IdleConnTimeout, dataGatewayIdleConnTimeout)
	}
}

// TestDataGatewayClientReusesConnections sends two bursts of parallel requests.
// The second burst must reuse the idle connections of the first burst.
// The default transport of Go keeps 2 idle connections for each host, so the
// second burst would open new connections.
func TestDataGatewayClientReusesConnections(t *testing.T) {
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

	client := NewCS3("gateway", "provider").dataGatewayClient

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
