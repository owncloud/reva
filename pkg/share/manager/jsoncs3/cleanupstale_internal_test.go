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
	"context"
	"errors"
	"testing"

	gatewayv1beta1 "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"google.golang.org/grpc"

	. "github.com/onsi/gomega"
)

// statGateway answers Stat from fixed sets of resource opaque IDs. Each
// other gateway call panics, because the embedded interface is nil.
type statGateway struct {
	gatewayv1beta1.GatewayAPIClient
	gone   map[string]bool // Stat returns NOT_FOUND
	broken map[string]bool // Stat returns a transport error
}

func (s statGateway) Stat(_ context.Context, req *provider.StatRequest, _ ...grpc.CallOption) (*provider.StatResponse, error) {
	id := req.GetRef().GetResourceId().GetOpaqueId()
	switch {
	case s.broken[id]:
		return nil, errors.New("injected transport error")
	case s.gone[id]:
		return &provider.StatResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_NOT_FOUND}}, nil
	default:
		return &provider.StatResponse{
			Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK},
			Info:   &provider.ResourceInfo{Id: req.GetRef().GetResourceId()},
		}, nil
	}
}

type statGatewaySelector struct{ gw statGateway }

func (s statGatewaySelector) Next(...pool.Option) (gatewayv1beta1.GatewayAPIClient, error) {
	return s.gw, nil
}

// spaceShareIDs returns the share IDs in the space file on storage.
func (f *batchFixture) spaceShareIDs(t *testing.T) []string {
	g := NewWithT(t)
	m, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	shares, err := m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	ids := []string{}
	for id := range shares.Shares {
		ids = append(ids, id)
	}
	return ids
}

// Break: the cleanup removes the orphaned shares one at a time. Each removal
// uploads the group file, the created file and the space file again, so 20
// orphaned shares in one space cost 60 uploads instead of 3.
func TestCleanupStaleSharesUploadsEachDocumentOnce(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	const orphans = 20
	gone := map[string]bool{}
	for n := 0; n < orphans; n++ {
		f.share(t, ctxOf("admin"), n, groupGrantee("group1"), nil)
		gone[resourceInfo(n).GetId().GetOpaqueId()] = true
	}
	f.share(t, ctxOf("admin"), orphans, groupGrantee("group1"), nil) // its resource exists
	valid := f.spaceShareIDs(t)
	g.Expect(valid).To(HaveLen(orphans + 1))
	f.m.gatewaySelector = statGatewaySelector{statGateway{gone: gone}}

	uploads := f.uploadsDuring(func() { f.m.CleanupStaleShares(ctxOf("admin")) })

	// one upload each: received.json of group1, created.json of admin, the space file
	g.Expect(uploads).To(Equal(int32(3)))
	left := f.spaceShareIDs(t)
	g.Expect(left).To(HaveLen(1), "only the share with an existing resource stays")
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(left[0]))
}

// Break: the cleanup reads the Stat response also after a Stat error. The
// response is nil, so one transport error stops the whole run with a panic.
func TestCleanupStaleSharesContinuesAfterAStatError(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	f.share(t, ctxOf("admin"), 1, groupGrantee("group1"), nil)
	f.m.gatewaySelector = statGatewaySelector{statGateway{
		broken: map[string]bool{resourceInfo(0).GetId().GetOpaqueId(): true},
		gone:   map[string]bool{resourceInfo(1).GetId().GetOpaqueId(): true},
	}}

	g.Expect(func() { f.m.CleanupStaleShares(ctxOf("admin")) }).ToNot(Panic())

	left := f.spaceShareIDs(t)
	g.Expect(left).To(HaveLen(1), "the cleanup removes the orphaned share and keeps the share with the Stat error")
	shares, err := f.m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(shares.Shares[left[0]].GetResourceId().GetOpaqueId()).To(Equal("file0"))
}

// Break: the cleanup continues without a gateway client and calls Stat on a
// nil client, so the run stops with a panic.
func TestCleanupStaleSharesStopsWithoutAGatewayClient(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	f.m.gatewaySelector = failingSelector{}

	var uploads int32
	g.Expect(func() {
		uploads = f.uploadsDuring(func() { f.m.CleanupStaleShares(ctxOf("admin")) })
	}).ToNot(Panic())
	g.Expect(uploads).To(BeZero())
	g.Expect(f.spaceShareIDs(t)).To(HaveLen(1))
}
