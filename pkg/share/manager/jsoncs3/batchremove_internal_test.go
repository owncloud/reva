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

// A loop that removes many shares must upload each touched document once,
// not once for each share. Each upload sends the whole document.

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	groupv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	microevents "go-micro.dev/v4/events"

	. "github.com/onsi/gomega"
)

// nopStream accepts all events. events.Publish needs a stream.
type nopStream struct{}

func (nopStream) Publish(string, interface{}, ...microevents.PublishOption) error { return nil }
func (nopStream) Consume(string, ...microevents.ConsumeOption) (<-chan microevents.Event, error) {
	return make(chan microevents.Event), nil
}

type batchFixture struct {
	storage metadata.Storage
	cs      *helpers.CountingUploadStorage
	m       *Manager
}

func newBatchFixture(t *testing.T) *batchFixture {
	g := NewWithT(t)
	dir, err := os.MkdirTemp("", "jsoncs3-batch")
	g.Expect(err).ToNot(HaveOccurred())
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	storage, err := metadata.NewDiskStorage(dir)
	g.Expect(err).ToNot(HaveOccurred())
	cs := &helpers.CountingUploadStorage{Storage: storage}
	m, err := New(cs, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	return &batchFixture{storage: storage, cs: cs, m: m}
}

func ctxOf(id string, groups ...string) context.Context {
	return ctxpkg.ContextSetUser(context.Background(), &userpb.User{Id: &userpb.UserId{OpaqueId: id}, Groups: groups})
}

func groupGrantee(gid string) *provider.Grantee {
	return &provider.Grantee{
		Type: provider.GranteeType_GRANTEE_TYPE_GROUP,
		Id:   &provider.Grantee_GroupId{GroupId: &groupv1beta1.GroupId{OpaqueId: gid}},
	}
}

func userGrantee(uid string) *provider.Grantee {
	return &provider.Grantee{
		Type: provider.GranteeType_GRANTEE_TYPE_USER,
		Id:   &provider.Grantee_UserId{UserId: &userpb.UserId{OpaqueId: uid}},
	}
}

var expiredAt = &typesv1beta1.Timestamp{Seconds: uint64(time.Now().Add(-time.Hour).Unix())}

// share creates a share of file n in space storageid$spaceid. A nil exp means no expiration.
func (f *batchFixture) share(t *testing.T, creator context.Context, n int, grantee *provider.Grantee, exp *typesv1beta1.Timestamp) {
	_, err := f.m.Share(creator, &provider.ResourceInfo{
		Id:    &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid", OpaqueId: fmt.Sprintf("file%d", n)},
		Owner: &userpb.UserId{OpaqueId: "owner"},
	}, &collaboration.ShareGrant{
		Grantee:     grantee,
		Permissions: &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true}},
		Expiration:  exp,
	})
	NewWithT(t).Expect(err).ToNot(HaveOccurred())
}

func (f *batchFixture) uploadsDuring(fn func()) int32 {
	before := atomic.LoadInt32(&f.cs.Uploads)
	fn()
	return atomic.LoadInt32(&f.cs.Uploads) - before
}

// empty checks on storage that no document has an entry for the space.
func (f *batchFixture) expectEmpty(t *testing.T, creators, groups, users []string) {
	g := NewWithT(t)
	m, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	ctx := ctxOf("admin")
	shares, err := m.Cache.ListSpace(ctx, "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(shares.Shares).To(BeEmpty(), "provider cache")
	for _, c := range creators {
		l, err := m.CreatedCache.List(ctx, c)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(l["storageid:spaceid"].IDs).To(BeEmpty(), "created.json of "+c)
	}
	for _, gid := range groups {
		l, err := m.GroupReceivedCache.List(ctx, gid)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(l["storageid:spaceid"].IDs).To(BeEmpty(), "received.json of group "+gid)
	}
	for _, u := range users {
		l, err := m.UserReceivedStates.List(ctx, u)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(l["storageid:spaceid"]).To(BeNil(), "received.json of user "+u)
	}
}

func TestPurgeSpaceUploadsEachDocumentOnce(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	for i := 0; i < 20; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), nil)
	}

	uploads := f.uploadsDuring(func() {
		g.Expect(f.m.purgeSpace(ctxOf("admin"), &provider.StorageSpaceId{OpaqueId: "storageid$spaceid"})).To(Succeed())
	})

	// group1 received.json, admin created.json, the provider file
	g.Expect(uploads).To(Equal(int32(3)))
	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
}

func TestPurgeSpaceWithManyCreatorsAndGranteesUploadsEachDocumentOnce(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	n := 0
	for _, creator := range []string{"admin", "teacher"} {
		for _, grantee := range []*provider.Grantee{groupGrantee("group1"), groupGrantee("group2"), userGrantee("einstein")} {
			for i := 0; i < 3; i++ {
				f.share(t, ctxOf(creator), n, grantee, nil)
				n++
			}
		}
	}

	uploads := f.uploadsDuring(func() {
		g.Expect(f.m.purgeSpace(ctxOf("admin"), &provider.StorageSpaceId{OpaqueId: "storageid$spaceid"})).To(Succeed())
	})

	// 2 created.json, 2 group files, 1 user received.json, the provider file
	g.Expect(uploads).To(Equal(int32(6)))
	f.expectEmpty(t, []string{"admin", "teacher"}, []string{"group1", "group2"}, []string{"einstein"})
}

func TestListCreatedSharesRemovesExpiredSharesWithOneUploadPerDocument(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	for i := 0; i < 20; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), expiredAt)
	}

	uploads := f.uploadsDuring(func() {
		shares, err := f.m.ListShares(ctxOf("admin"), nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(shares).To(BeEmpty())
	})

	g.Expect(uploads).To(Equal(int32(3)))
	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
}

func TestListSharesByIDsRemovesExpiredSharesWithOneUploadPerDocument(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	for i := 0; i < 20; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), expiredAt)
	}

	uploads := f.uploadsDuring(func() {
		shares, err := f.m.ListShares(ctxOf("admin"), []*collaboration.Filter{{
			Type: collaboration.Filter_TYPE_RESOURCE_ID,
			Term: &collaboration.Filter_ResourceId{ResourceId: &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid"}},
		}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(shares).To(BeEmpty())
	})

	g.Expect(uploads).To(Equal(int32(3)))
	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
}

func TestListReceivedSharesRemovesExpiredSharesWithOneUploadPerDocument(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	for i := 0; i < 20; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), expiredAt)
	}

	uploads := f.uploadsDuring(func() {
		received, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(received).To(BeEmpty())
	})

	g.Expect(uploads).To(Equal(int32(3)))
	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
}
