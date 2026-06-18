package storageprovider

import (
	"context"
	"net/url"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storagespace"
)

// stubFS overrides only the methods under test; the embedded storage.FS satisfies the rest.
type stubFS struct {
	storage.FS
	listSpaces  func(ctx context.Context, filters []*provider.ListStorageSpacesRequest_Filter, unrestricted bool) ([]*provider.StorageSpace, error)
	deleteSpace func(ctx context.Context, req *provider.DeleteStorageSpaceRequest) (*storage.DeleteStorageSpaceResult, error)
}

func (s *stubFS) ListStorageSpaces(ctx context.Context, filters []*provider.ListStorageSpacesRequest_Filter, unrestricted bool) ([]*provider.StorageSpace, error) {
	return s.listSpaces(ctx, filters, unrestricted)
}

func (s *stubFS) DeleteStorageSpace(ctx context.Context, req *provider.DeleteStorageSpaceRequest) (*storage.DeleteStorageSpaceResult, error) {
	if s.deleteSpace != nil {
		return s.deleteSpace(ctx, req)
	}
	return nil, nil
}

func (s *stubFS) Shutdown(_ context.Context) error { return nil }

// CreateReference is called by the storageprovider on some paths; stub to avoid nil-embed panic.
func (s *stubFS) CreateReference(_ context.Context, _ string, _ *url.URL) error { return nil }

var _ = Describe("DeleteStorageSpace", func() {
	var (
		svc *Service
		ctx context.Context
	)

	space := &provider.StorageSpace{
		Id:   &provider.StorageSpaceId{OpaqueId: "providerid$spaceid!spaceid"},
		Name: "My Space",
		Root: &provider.ResourceId{StorageId: "providerid", SpaceId: "spaceid", OpaqueId: "spaceid"},
	}

	req := &provider.DeleteStorageSpaceRequest{
		Id: &provider.StorageSpaceId{OpaqueId: "providerid$spaceid!spaceid"},
	}

	listFound := func(_ context.Context, _ []*provider.ListStorageSpacesRequest_Filter, _ bool) ([]*provider.StorageSpace, error) {
		return []*provider.StorageSpace{space}, nil
	}

	BeforeEach(func() {
		ctx = storagespace.ContextRegisterDeleteStorageSpaceResultSlot(context.Background())
	})

	Context("when driver returns nil result (non-decomposedfs path)", func() {
		BeforeEach(func() {
			svc = &Service{Storage: &stubFS{listSpaces: listFound}}
		})

		It("populates SpaceName from pre-fetched space so SpaceDeleted event is not empty", func() {
			res, err := svc.DeleteStorageSpace(ctx, req)

			Expect(err).ToNot(HaveOccurred())
			Expect(res.Status.Code).To(Equal(rpc.Code_CODE_OK))

			result := storagespace.ContextGetDeleteStorageSpaceResult(ctx)
			Expect(result).ToNot(BeNil())
			Expect(result.SpaceName).To(Equal("My Space"))
			Expect(result.FinalMembers).To(BeNil())
		})

		It("does not carry spacename/grants in response opaque — consumers read from context slot", func() {
			res, err := svc.DeleteStorageSpace(ctx, req)

			Expect(err).ToNot(HaveOccurred())
			Expect(res.Opaque).To(BeNil())
		})
	})

	Context("when driver returns a populated result (decomposedfs path)", func() {
		finalMembers := map[string]provider.ResourcePermissions{
			"user-1": {Stat: true},
		}

		BeforeEach(func() {
			svc = &Service{Storage: &stubFS{
				listSpaces: listFound,
				deleteSpace: func(_ context.Context, _ *provider.DeleteStorageSpaceRequest) (*storage.DeleteStorageSpaceResult, error) {
					return &storage.DeleteStorageSpaceResult{SpaceName: "Decomposed Space", FinalMembers: finalMembers}, nil
				},
			}}
		})

		It("uses driver result as-is, preserving FinalMembers", func() {
			res, err := svc.DeleteStorageSpace(ctx, req)

			Expect(err).ToNot(HaveOccurred())
			Expect(res.Status.Code).To(Equal(rpc.Code_CODE_OK))

			result := storagespace.ContextGetDeleteStorageSpaceResult(ctx)
			Expect(result).ToNot(BeNil())
			Expect(result.SpaceName).To(Equal("Decomposed Space"))
			Expect(result.FinalMembers).To(HaveKey("user-1"))
		})
	})

	Context("when space is not found", func() {
		BeforeEach(func() {
			svc = &Service{Storage: &stubFS{
				listSpaces: func(_ context.Context, _ []*provider.ListStorageSpacesRequest_Filter, _ bool) ([]*provider.StorageSpace, error) {
					return []*provider.StorageSpace{}, nil
				},
			}}
		})

		It("returns NOT_FOUND without calling DeleteStorageSpace", func() {
			res, err := svc.DeleteStorageSpace(ctx, req)

			Expect(err).ToNot(HaveOccurred())
			Expect(res.Status.Code).To(Equal(rpc.Code_CODE_NOT_FOUND))
		})
	})
})
