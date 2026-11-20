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

package jsoncs3

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gatewayv1beta1 "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/rs/zerolog"

	. "github.com/onsi/gomega"
)

// failingSelector fails each selection of a gateway client.
type failingSelector struct{}

func (failingSelector) Next(...pool.Option) (gatewayv1beta1.GatewayAPIClient, error) {
	return nil, errors.New("injected")
}

// Break: the listing adds the resource fields to the logger for each share
// that it reads. A later log line then has the fields of all earlier shares,
// and a log search shows the wrong resource.
func TestListSharesByIDsLogsTheFieldsOfOneShare(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.gatewaySelector = failingSelector{}
	var filters []*collaboration.Filter
	for n := range 3 {
		f.share(t, ctxOf("admin"), n, userGrantee("einstein"), nil)
		filters = append(filters, &collaboration.Filter{
			Type: collaboration.Filter_TYPE_RESOURCE_ID,
			Term: &collaboration.Filter_ResourceId{ResourceId: resourceInfo(n).GetId()},
		})
	}

	// marie neither created nor received the shares, so the listing checks
	// her permissions with the gateway, and the gateway selection fails.
	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.ErrorLevel)
	_, err := f.m.ListShares(appctx.WithLogger(ctxOf("marie"), &logger), filters)
	g.Expect(err).ToNot(HaveOccurred())

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	g.Expect(lines).To(HaveLen(3), buf.String())
	opaqueIDs := map[string]bool{}
	for _, line := range lines {
		for _, key := range []string{"storageid", "spaceid", "opaqueid"} {
			g.Expect(strings.Count(line, `"`+key+`":`)).To(Equal(1), key+" in "+line)
		}
		var entry struct {
			OpaqueID string `json:"opaqueid"`
		}
		g.Expect(json.Unmarshal([]byte(line), &entry)).To(Succeed())
		opaqueIDs[entry.OpaqueID] = true
	}
	g.Expect(opaqueIDs).To(HaveLen(3), "each line names its own resource")
}
