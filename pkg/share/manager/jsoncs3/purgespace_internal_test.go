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

// This file uses the internal package, because purgeSpace is not exported.

import (
	"context"
	"os"
	"testing"

	groupv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

type purgeFixture struct {
	storage metadata.Storage
	cs      *helpers.CountingUploadStorage
	m       *Manager
	ctx     context.Context
	share   *collaboration.Share
}

// newPurgeFixture creates one group share in the space storageid$spaceid.
func newPurgeFixture(t *testing.T) *purgeFixture {
	g := NewWithT(t)

	dir, err := os.MkdirTemp("", "jsoncs3-purge")
	g.Expect(err).ToNot(HaveOccurred())
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	storage, err := metadata.NewDiskStorage(dir)
	g.Expect(err).ToNot(HaveOccurred())
	cs := &helpers.CountingUploadStorage{Storage: storage}
	m, err := New(cs, nil, 0, nil, 0)
	g.Expect(err).ToNot(HaveOccurred())

	owner := &userpb.User{Id: &userpb.UserId{OpaqueId: "admin"}}
	ctx := ctxpkg.ContextSetUser(context.Background(), owner)
	s, err := m.Share(ctx,
		&provider.ResourceInfo{
			Id:    &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid", OpaqueId: "file"},
			Owner: owner.Id,
		},
		&collaboration.ShareGrant{
			Grantee: &provider.Grantee{
				Type: provider.GranteeType_GRANTEE_TYPE_GROUP,
				Id:   &provider.Grantee_GroupId{GroupId: &groupv1beta1.GroupId{OpaqueId: "group1"}},
			},
			Permissions: &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true}},
		})
	g.Expect(err).ToNot(HaveOccurred())

	return &purgeFixture{storage: storage, cs: cs, m: m, ctx: ctx, share: s}
}

// fresh returns a manager with empty in-memory caches, to read the state on storage.
func (f *purgeFixture) fresh(t *testing.T) *Manager {
	m, err := New(f.storage, nil, 0, nil, 0)
	NewWithT(t).Expect(err).ToNot(HaveOccurred())
	return m
}

var spaceToPurge = &provider.StorageSpaceId{OpaqueId: "storageid$spaceid"}

func TestPurgeSpaceRemovesAllEntries(t *testing.T) {
	g := NewWithT(t)
	f := newPurgeFixture(t)

	g.Expect(f.m.purgeSpace(f.ctx, spaceToPurge)).To(Succeed())

	m := f.fresh(t)
	shares, err := m.Cache.ListSpace(f.ctx, "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(shares.Shares).To(BeEmpty())
	created, err := m.CreatedCache.List(f.ctx, "admin")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(created["storageid:spaceid"].IDs).To(BeEmpty())
	group, err := m.GroupReceivedCache.List(f.ctx, "group1")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(group["storageid:spaceid"].IDs).To(BeEmpty())
}

// The space is gone. When the group cache cannot be updated, the purge must
// still remove the created.json entry. Otherwise the creator keeps the ID forever.
func TestPurgeSpaceRemovesCreatedEntryWhenGroupRemovalFails(t *testing.T) {
	g := NewWithT(t)
	f := newPurgeFixture(t)

	f.cs.FailPath = "/groups/group1/received.json"
	_ = f.m.purgeSpace(f.ctx, spaceToPurge)
	f.cs.FailPath = ""

	created, err := f.fresh(t).CreatedCache.List(f.ctx, "admin")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(created["storageid:spaceid"].IDs).ToNot(HaveKey(f.share.Id.OpaqueId))
}

// The purge must report each failure to the caller, with the ID of the share.
func TestPurgeSpaceReturnsTheRemovalErrors(t *testing.T) {
	g := NewWithT(t)
	f := newPurgeFixture(t)

	f.cs.FailPath = "/groups/group1/received.json"
	err := f.m.purgeSpace(f.ctx, spaceToPurge)
	f.cs.FailPath = ""

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring(f.share.Id.OpaqueId))
}

// The space is gone, so the purge must empty the provider cache also after a failure.
func TestPurgeSpacePurgesTheSpaceWhenGroupRemovalFails(t *testing.T) {
	g := NewWithT(t)
	f := newPurgeFixture(t)

	f.cs.FailPath = "/groups/group1/received.json"
	_ = f.m.purgeSpace(f.ctx, spaceToPurge)
	f.cs.FailPath = ""

	shares, err := f.fresh(t).Cache.ListSpace(f.ctx, "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(shares.Shares).To(BeEmpty())
}
