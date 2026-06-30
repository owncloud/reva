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

package decomposedfs_test

import (
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	helpers "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/testhelpers"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Revisions", func() {
	var (
		env *helpers.TestEnv
	)

	JustBeforeEach(func() {
		var err error
		env, err = helpers.NewTestEnv(nil)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if env != nil {
			env.Cleanup()
		}
	})

	Describe("RestoreRevision", func() {
		Context("on a node marked as processing", func() {
			It("rejects with ResourceProcessing", func() {
				fileRef := &provider.Reference{
					ResourceId: env.SpaceRootRes,
					Path:       "/dir1/file1",
				}
				env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(&provider.ResourcePermissions{
					Stat:               true,
					RestoreFileVersion: true,
				}, nil)

				n, err := env.Lookup.NodeFromResource(env.Ctx, fileRef)
				Expect(err).ToNot(HaveOccurred())

				Expect(env.Fs.MarkProcessing(env.Ctx, fileRef, true, "test-session")).To(Succeed())

				revisionKey := n.ID + node.RevisionIDDelimiter + "2026-01-01T00:00:00Z"
				_, err = env.Fs.RestoreRevision(env.Ctx, fileRef, revisionKey)

				Expect(err).To(HaveOccurred())
				_, ok := err.(errtypes.IsResourceProcessing)
				Expect(ok).To(BeTrue(), "expected errtypes.ResourceProcessing, got %T: %v", err, err)
			})
		})
	})
})
