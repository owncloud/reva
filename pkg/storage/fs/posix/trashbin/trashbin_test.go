// Copyright 2018-2024 CERN
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

package trashbin_test

import (
	"github.com/stretchr/testify/mock"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	helpers "github.com/owncloud/reva/v2/pkg/storage/fs/posix/testhelpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// grantTrashPermissions makes the permissions mock answer every
// AssembleTrashPermissions call with the given permissions.
func grantTrashPermissions(env *helpers.TestEnv, rp *provider.ResourcePermissions) {
	env.Permissions.On("AssembleTrashPermissions", mock.Anything, mock.Anything).Return(rp, nil)
}

var _ = Describe("Trashbin", func() {
	var (
		env *helpers.TestEnv
		ref *provider.Reference
	)

	BeforeEach(func() {
		var err error
		env, err = helpers.NewTestEnv(map[string]interface{}{
			"metadata_backend": "messagepack",
		})
		Expect(err).ToNot(HaveOccurred())

		// recycle operations address the space, not the deleted item
		ref = &provider.Reference{ResourceId: env.SpaceRootRes}
	})

	AfterEach(func() {
		if env != nil {
			env.Cleanup()
		}
	})

	Context("when the user is not a member of the space", func() {
		BeforeEach(func() {
			// AssembleTrashPermissions accumulates grants the user actually holds,
			// so a non-member ends up with no permissions at all
			grantTrashPermissions(env, &provider.ResourcePermissions{})
		})

		It("does not confirm that the space exists when listing", func() {
			_, err := env.Fs.ListRecycle(env.Ctx, ref, "", "")
			Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
		})

		It("does not confirm that the space exists when restoring", func() {
			_, err := env.Fs.RestoreRecycleItem(env.Ctx, ref, "key", "", ref)
			Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
		})

		It("does not confirm that the space exists when purging", func() {
			err := env.Fs.PurgeRecycleItem(env.Ctx, ref, "key", "")
			Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
		})

		It("does not confirm that the space exists when emptying the trash", func() {
			err := env.Fs.EmptyRecycle(env.Ctx, ref)
			Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
		})
	})

	Context("when the user is a viewer", func() {
		BeforeEach(func() {
			// a space viewer may browse the trash but not change it,
			// see conversions.NewSpaceViewerRole()
			grantTrashPermissions(env, &provider.ResourcePermissions{
				Stat:        true,
				ListRecycle: true,
			})
		})

		It("allows listing the trash", func() {
			items, err := env.Fs.ListRecycle(env.Ctx, ref, "", "")
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(BeEmpty())
		})

		It("denies restoring", func() {
			_, err := env.Fs.RestoreRecycleItem(env.Ctx, ref, "key", "", ref)
			Expect(err).To(BeAssignableToTypeOf(errtypes.PermissionDenied("")))
		})

		It("denies purging a single item", func() {
			err := env.Fs.PurgeRecycleItem(env.Ctx, ref, "key", "")
			Expect(err).To(BeAssignableToTypeOf(errtypes.PermissionDenied("")))
		})

		It("denies emptying the trash", func() {
			err := env.Fs.EmptyRecycle(env.Ctx, ref)
			Expect(err).To(BeAssignableToTypeOf(errtypes.PermissionDenied("")))
		})
	})

	Context("when the user is an editor", func() {
		BeforeEach(func() {
			grantTrashPermissions(env, &provider.ResourcePermissions{
				Stat:               true,
				ListRecycle:        true,
				RestoreRecycleItem: true,
				PurgeRecycle:       true,
			})
		})

		It("allows listing the trash", func() {
			items, err := env.Fs.ListRecycle(env.Ctx, ref, "", "")
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(BeEmpty())
		})

		It("allows emptying the trash", func() {
			Expect(env.Fs.EmptyRecycle(env.Ctx, ref)).To(Succeed())
		})

		// the item does not exist so these still fail, but they have to fail
		// on the missing item rather than on the permission check
		It("lets restoring pass the permission check", func() {
			_, err := env.Fs.RestoreRecycleItem(env.Ctx, ref, "does-not-exist", "", ref)
			Expect(err).To(HaveOccurred())
			Expect(err).ToNot(BeAssignableToTypeOf(errtypes.PermissionDenied("")))
			Expect(err).ToNot(BeAssignableToTypeOf(errtypes.NotFound("")))
		})

		It("lets purging pass the permission check", func() {
			err := env.Fs.PurgeRecycleItem(env.Ctx, ref, "does-not-exist", "")
			Expect(err).To(HaveOccurred())
			Expect(err).ToNot(BeAssignableToTypeOf(errtypes.PermissionDenied("")))
			Expect(err).ToNot(BeAssignableToTypeOf(errtypes.NotFound("")))
		})
	})
})
