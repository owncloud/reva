package kiteworks_test

import (
	"errors"
	"fmt"
	"math"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/utils"
)

func spaceNames(spaces []*provider.StorageSpace) []string {
	names := make([]string, 0, len(spaces))
	for _, s := range spaces {
		names = append(names, s.Name)
	}
	return names
}

var _ = Describe("kiteworks driver spaces", func() {
	var (
		d    storage.FS
		fix  *fixture
		stop func()
	)

	BeforeEach(func() {
		d, fix, stop = setupDriver()
	})

	AfterEach(func() {
		stop()
	})

	Context("read path", func() {
		Describe("ListStorageSpaces", func() {
			It("returns the personal space identified by syncdirId", func() {
				skipIfRealBox()
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).ToNot(BeEmpty())
				Expect(spaces[0].SpaceType).To(Equal("personal"))
				Expect(spaces[0].Name).ToNot(BeEmpty())
			})

			It("returns space with root ResourceId storageID=kiteworks", func() {
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces[0].Root.StorageId).To(Equal("kiteworks"))
			})

			It("includes deleted spaces with trashed opaque marker", func() {
				skipIfRealBox()
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				var trashed *provider.StorageSpace
				for _, s := range spaces {
					if s.Name == "Deleted Space" {
						trashed = s
						break
					}
				}
				Expect(trashed).ToNot(BeNil())
				Expect(trashed.Opaque.GetMap()["trashed"]).ToNot(BeNil())
			})

			DescribeTable("applies the space type filter",
				func(spaceTypes []string, expected []string) {
					skipIfRealBox()
					filters := make([]*provider.ListStorageSpacesRequest_Filter, 0, len(spaceTypes))
					for _, t := range spaceTypes {
						filters = append(filters, &provider.ListStorageSpacesRequest_Filter{
							Type: provider.ListStorageSpacesRequest_Filter_TYPE_SPACE_TYPE,
							Term: &provider.ListStorageSpacesRequest_Filter_SpaceType{SpaceType: t},
						})
					}
					spaces, err := d.ListStorageSpaces(fix.ctx, filters, false)
					Expect(err).ToNot(HaveOccurred())
					Expect(spaceNames(spaces)).To(ConsistOf(expected))
				},
				Entry("no filter returns every space", nil, []string{"My Docs", "Deleted Space"}),
				Entry("personal returns only the syncdir space", []string{"personal"}, []string{"My Docs"}),
				Entry("project excludes the syncdir space", []string{"project"}, []string{"Deleted Space"}),
				Entry("both types return every space", []string{"personal", "project"}, []string{"My Docs", "Deleted Space"}),
				Entry("an unknown type returns nothing", []string{"mountpoint"}, []string{}),
				Entry("+grant is not treated as a type", []string{"+grant"}, []string{"My Docs", "Deleted Space"}),
			)

			It("applies the space type filter alongside an ID filter", func() {
				skipIfRealBox()
				idFilter := &provider.ListStorageSpacesRequest_Filter{
					Type: provider.ListStorageSpacesRequest_Filter_TYPE_ID,
					Term: &provider.ListStorageSpacesRequest_Filter_Id{
						Id: &provider.StorageSpaceId{OpaqueId: "kiteworks$space-1"},
					},
				}
				typeFilter := func(t string) *provider.ListStorageSpacesRequest_Filter {
					return &provider.ListStorageSpacesRequest_Filter{
						Type: provider.ListStorageSpacesRequest_Filter_TYPE_SPACE_TYPE,
						Term: &provider.ListStorageSpacesRequest_Filter_SpaceType{SpaceType: t},
					}
				}

				spaces, err := d.ListStorageSpaces(fix.ctx, []*provider.ListStorageSpacesRequest_Filter{idFilter, typeFilter("personal")}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaceNames(spaces)).To(ConsistOf("My Docs"))

				// the type filter must apply regardless of where it sits in the list
				spaces, err = d.ListStorageSpaces(fix.ctx, []*provider.ListStorageSpacesRequest_Filter{idFilter, typeFilter("project")}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())
			})

			It("still lists active spaces when the deleted listing is denied", func() {
				skipIfRealBox()
				fix.mock.failDeletedTop.Store(true)
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaceNames(spaces)).To(ConsistOf("My Docs"))
			})

			It("falls back to project spaces when the syncdir lookup is denied", func() {
				skipIfRealBox()
				fix.mock.failMe.Store(true)
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).ToNot(BeEmpty())
				for _, s := range spaces {
					Expect(s.SpaceType).To(Equal("project"))
				}
			})
		})
	})

	Context("space lifecycle", func() {
		BeforeEach(func() { skipIfRealBox() })

		Describe("CreateStorageSpace", func() {
			It("creates a top-level folder and returns a StorageSpace", func() {
				resp, err := d.CreateStorageSpace(fix.ctx, &provider.CreateStorageSpaceRequest{
					Name: "New Space",
					Type: "project",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp).ToNot(BeNil())
				Expect(resp.StorageSpace).ToNot(BeNil())
				Expect(resp.StorageSpace.Name).To(Equal("New Space"))
				Expect(resp.StorageSpace.Id.OpaqueId).To(Equal("kiteworks$new-space-1"))
				Expect(resp.StorageSpace.SpaceType).To(Equal("project"))
			})

			It("returns NotSupported for personal type", func() {
				_, err := d.CreateStorageSpace(fix.ctx, &provider.CreateStorageSpaceRequest{
					Type: "personal",
					Name: "Test User",
				})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.NotSupported))).To(BeTrue())
			})

			It("returns NotSupported for any other type", func() {
				_, err := d.CreateStorageSpace(fix.ctx, &provider.CreateStorageSpaceRequest{
					Type: "mountpoint",
					Name: "New Space",
				})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.NotSupported))).To(BeTrue())
			})

			It("returns BadRequest when name is empty", func() {
				_, err := d.CreateStorageSpace(fix.ctx, &provider.CreateStorageSpaceRequest{})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.BadRequest))).To(BeTrue())
			})
		})

		Describe("UpdateStorageSpace", func() {
			It("renames the folder and returns the updated StorageSpace", func() {
				resp, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{
					StorageSpace: &provider.StorageSpace{
						Id:   &provider.StorageSpaceId{OpaqueId: "space-rename-1"},
						Name: "Renamed Space",
					},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp).ToNot(BeNil())
				Expect(resp.StorageSpace).ToNot(BeNil())
				Expect(resp.StorageSpace.Name).To(Equal("Renamed Space"))
				Expect(resp.StorageSpace.Id.OpaqueId).To(Equal("kiteworks$space-rename-1"))
			})

			It("returns BadRequest when space is nil", func() {
				_, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.BadRequest))).To(BeTrue())
			})

			It("recovers a deleted space when the restore opaque is set", func() {
				resp, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{
					Opaque: utils.AppendPlainToOpaque(nil, "restore", "true"),
					StorageSpace: &provider.StorageSpace{
						Id: &provider.StorageSpaceId{OpaqueId: "kiteworks$space-restore-1"},
					},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.StorageSpace.Opaque.GetMap()).ToNot(HaveKey("trashed"))
			})

			It("leaves an already active space untouched instead of failing", func() {
				req := &provider.UpdateStorageSpaceRequest{
					Opaque: utils.AppendPlainToOpaque(nil, "restore", "true"),
					StorageSpace: &provider.StorageSpace{
						Id: &provider.StorageSpaceId{OpaqueId: "kiteworks$space-restore-1"},
					},
				}
				_, err := d.UpdateStorageSpace(fix.ctx, req)
				Expect(err).ToNot(HaveOccurred())
				_, err = d.UpdateStorageSpace(fix.ctx, req)
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns PermissionDenied when folder_recover is not granted", func() {
				_, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{
					Opaque: utils.AppendPlainToOpaque(nil, "restore", "true"),
					StorageSpace: &provider.StorageSpace{
						Id: &provider.StorageSpaceId{OpaqueId: "kiteworks$space-no-recover-1"},
					},
				})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.PermissionDenied))).To(BeTrue())
			})

			It("keeps the trashed marker on a space that stays deleted", func() {
				resp, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{
					StorageSpace: &provider.StorageSpace{
						Id: &provider.StorageSpaceId{OpaqueId: "kiteworks$space-no-recover-1"},
					},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.StorageSpace.Opaque.GetMap()).To(HaveKey("trashed"))
			})

			DescribeTable("snaps the requested quota onto a permitted KW value",
				func(requested uint64, expected int64) {
					_, err := d.UpdateStorageSpace(fix.ctx, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Id:    &provider.StorageSpaceId{OpaqueId: "kiteworks$space-quota-1"},
							Quota: &provider.Quota{QuotaMaxBytes: requested},
						},
					})
					Expect(err).ToNot(HaveOccurred())
					Expect(fix.mock.quotaBody.Load()).To(Equal(
						fmt.Sprintf(`{"useFolderQuota":true,"quota":%d}`, expected)))
				},
				Entry("unrestricted stays unrestricted", uint64(0), int64(-1)),
				Entry("a single byte takes the smallest quota", uint64(1), int64(1<<30)),
				Entry("an exact permitted value is kept", uint64(2<<30), int64(2<<30)),
				Entry("3 GiB snaps up to 5 GiB", uint64(3<<30), int64(5<<30)),
				Entry("20 GiB snaps up to 50 GiB", uint64(20<<30), int64(50<<30)),
				Entry("the largest permitted value is kept", uint64(50<<30), int64(50<<30)),
				Entry("above the largest snaps down, not to unlimited", uint64(50<<30)+1, int64(50<<30)),
				Entry("1 TiB snaps down to 50 GiB", uint64(1<<40), int64(50<<30)),
				Entry("MaxUint64 does not wrap to the smallest quota", uint64(math.MaxUint64), int64(50<<30)),
			)
		})

		Describe("DeleteStorageSpace", func() {
			It("deletes the top-level folder and returns the space name", func() {
				result, err := d.DeleteStorageSpace(fix.ctx, &provider.DeleteStorageSpaceRequest{
					Id: &provider.StorageSpaceId{OpaqueId: "new-space-1"},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.SpaceName).To(Equal("New Space"))
				Expect(fix.mock.permDeleted.Load()).To(BeFalse())
			})

			It("returns NotFound for an unknown space", func() {
				_, err := d.DeleteStorageSpace(fix.ctx, &provider.DeleteStorageSpaceRequest{
					Id: &provider.StorageSpaceId{OpaqueId: "does-not-exist"},
				})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.NotFound))).To(BeTrue())
			})

			It("hard-deletes when purge opaque flag is set", func() {
				result, err := d.DeleteStorageSpace(fix.ctx, &provider.DeleteStorageSpaceRequest{
					Id:     &provider.StorageSpaceId{OpaqueId: "new-space-1"},
					Opaque: utils.AppendPlainToOpaque(nil, "purge", ""),
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.SpaceName).To(Equal("New Space"))
				Expect(fix.mock.permDeleted.Load()).To(BeTrue())
			})

			It("returns BadRequest when ID is empty", func() {
				_, err := d.DeleteStorageSpace(fix.ctx, &provider.DeleteStorageSpaceRequest{
					Id: &provider.StorageSpaceId{},
				})
				Expect(err).To(HaveOccurred())
				Expect(errors.As(err, new(errtypes.BadRequest))).To(BeTrue())
			})
		})
	})
})
