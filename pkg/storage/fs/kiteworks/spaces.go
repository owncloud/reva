package kiteworks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/fs/kiteworks/kwlib"
	"github.com/owncloud/reva/v2/pkg/storagespace"
	"github.com/owncloud/reva/v2/pkg/utils"
)

func (d *Driver) ListStorageSpaces(ctx context.Context, filters []*provider.ListStorageSpacesRequest_Filter, _ bool) ([]*provider.StorageSpace, error) {
	c := d.client(ctx)

	personalFolderID := d.personalFolderID(c)

	// collected before the ID filter is handled below: a type filter may sit after it
	var spaceTypes []string
	for _, f := range filters {
		// "+grant"/"+mountpoint" ask to include extra types, they are not types themselves
		if t := f.GetSpaceType(); f.GetType() == provider.ListStorageSpacesRequest_Filter_TYPE_SPACE_TYPE && !strings.HasPrefix(t, "+") {
			spaceTypes = append(spaceTypes, t)
		}
	}
	wanted := func(s *provider.StorageSpace) bool {
		return len(spaceTypes) == 0 || slices.Contains(spaceTypes, s.GetSpaceType())
	}

	for _, f := range filters {
		if f.GetType() == provider.ListStorageSpacesRequest_Filter_TYPE_ID {
			_, spaceID, _, _ := storagespace.SplitID(f.GetId().GetOpaqueId())
			if spaceID == "" {
				return nil, nil
			}
			fi, err := c.GetFolderByID(spaceID)
			if err != nil {
				var ce *kwlib.ClientError
				if errors.As(err, &ce) && ce.StatusCode == http.StatusNotFound {
					return nil, nil
				}
				return nil, err
			}
			space := d.toStorageSpace(ctx, fi, personalFolderID)
			if !wanted(space) {
				return nil, nil
			}
			return []*provider.StorageSpace{space}, nil
		}
	}

	dirs, err := c.GetTopFolders(false)
	if err != nil {
		return nil, err
	}
	// best effort: disabled spaces are an addition to the listing, not a reason to fail it
	deleted, err := c.GetTopFolders(true)
	if err != nil {
		d.log.Warn().Err(err).Msg("kiteworks: could not list deleted top folders, omitting disabled spaces")
		deleted = &kwlib.DirectoryInfo{}
	}

	spaces := make([]*provider.StorageSpace, 0, len(dirs.Data)+len(deleted.Data))
	for _, fi := range slices.Concat(dirs.Data, deleted.Data) {
		if space := d.toStorageSpace(ctx, &fi, personalFolderID); wanted(space) {
			spaces = append(spaces, space)
		}
	}

	return spaces, nil
}

// personalFolderID returns the syncdir folder id, or "" if it cannot be resolved,
// in which case every space is reported as a project space.
func (d *Driver) personalFolderID(c *kwlib.APIClient) string {
	me, err := c.GetMe() //TODO: cache this?
	if err != nil {
		d.log.Warn().Err(err).Msg("kiteworks: could not resolve syncdirId")
		return ""
	}
	return me.SyncDirID
}

func (d *Driver) toStorageSpace(ctx context.Context, fi *kwlib.FileInfo, personalFolderID string) *provider.StorageSpace {
	u, hasUser := ctxpkg.ContextGetUser(ctx)
	spaceType := "project"
	if personalFolderID != "" && fi.ID == personalFolderID {
		spaceType = "personal"
	}
	opaque := utils.AppendPlainToOpaque(nil, "spaceAlias", spaceType+"/"+fi.Name)
	if fi.Deleted {
		opaque = utils.AppendPlainToOpaque(opaque, "trashed", "trashed")
	}
	if hasUser && u.GetId().GetOpaqueId() != "" {
		grants := map[string]*provider.ResourcePermissions{
			u.Id.OpaqueId: spaceRole(fi),
		}
		if b, err := json.Marshal(grants); err == nil {
			opaque.Map["grants"] = &types.OpaqueEntry{Decoder: "json", Value: b}
		}
	}
	space := &provider.StorageSpace{
		Id:        &provider.StorageSpaceId{OpaqueId: storagespace.FormatStorageID(d.storageID, fi.ID)},
		Name:      fi.Name,
		SpaceType: spaceType,
		Root: &provider.ResourceId{
			StorageId: d.storageID,
			SpaceId:   fi.ID,
			OpaqueId:  fi.ID,
		},
		RootInfo: d.toResourceInfo(fi, fi.ID, fi.Path),
		Mtime:    utils.TimeToTS(fi.MTime()),
		Opaque:   opaque,
	}
	if spaceType == "personal" && hasUser {
		space.Owner = u
	}
	return space
}

func (d *Driver) CreateStorageSpace(ctx context.Context, req *provider.CreateStorageSpaceRequest) (*provider.CreateStorageSpaceResponse, error) {
	c := d.client(ctx)

	switch req.GetType() {
	case "", "project":
	case "personal":
		// KW has no API to create personal folders; they are provisioned by KW on user login.
		return nil, errtypes.NotSupported("kiteworks: personal space creation is not supported")
	default:
		return nil, errtypes.NotSupported("kiteworks: only project spaces can be created, got " + req.GetType())
	}

	name := req.GetName()
	if name == "" {
		return nil, errtypes.BadRequest("kiteworks: CreateStorageSpace requires a name")
	}
	folderID, err := c.CreateFolder("0", kwlib.CreateDirRequest{Name: name})
	if err != nil {
		var ce *kwlib.ClientError
		if errors.As(err, &ce) && ce.StatusCode == http.StatusConflict {
			return nil, errtypes.AlreadyExists(name)
		}
		return nil, err
	}
	if folderID == "" {
		return nil, errtypes.InternalError("kiteworks: folder created but no location header returned")
	}
	fi, err := c.GetFolderByID(folderID)
	if err != nil {
		return nil, err
	}
	return &provider.CreateStorageSpaceResponse{
		Status:       &rpc.Status{Code: rpc.Code_CODE_OK},
		StorageSpace: d.toStorageSpace(ctx, fi, ""),
	}, nil
}

// kwPermittedQuotas are the only per-folder quota values KW accepts, in bytes.
// KW encodes unlimited as -1.
var kwPermittedQuotas = []int64{
	1_073_741_824,  // 1 GiB
	2_147_483_648,  // 2 GiB
	5_368_709_120,  // 5 GiB
	10_737_418_240, // 10 GiB
	53_687_091_200, // 50 GiB
}

// snapToKWQuota maps a requested quota onto a permitted KW value: up to the next
// permitted value, or down to the largest one if the request exceeds it. Only an
// unrestricted request (0) maps to unlimited.
func snapToKWQuota(bytes uint64) int64 {
	if bytes == 0 {
		return -1
	}
	for _, permitted := range kwPermittedQuotas {
		if bytes <= uint64(permitted) {
			return permitted
		}
	}
	return kwPermittedQuotas[len(kwPermittedQuotas)-1]
}

func (d *Driver) UpdateStorageSpace(ctx context.Context, req *provider.UpdateStorageSpaceRequest) (*provider.UpdateStorageSpaceResponse, error) {
	space := req.GetStorageSpace()
	if space == nil {
		return nil, errtypes.BadRequest("kiteworks: UpdateStorageSpace requires a StorageSpace")
	}
	_, spaceID, _, _ := storagespace.SplitID(space.GetId().GetOpaqueId())
	if spaceID == "" {
		return nil, errtypes.BadRequest("kiteworks: UpdateStorageSpace requires a space ID")
	}
	c := d.client(ctx)
	fi, err := c.GetFolderByID(spaceID)
	if err != nil {
		return nil, err
	}
	if _, restore := req.GetOpaque().GetMap()["restore"]; restore && fi.Deleted {
		if !fi.HasPermission(kwlib.PermFolderRecover) {
			return nil, errtypes.PermissionDenied(spaceID)
		}
		if err := c.RecoverFolder(spaceID); err != nil {
			return nil, err
		}
	}
	if space.Name != "" && space.Name != fi.Name {
		if _, err := c.RenameFolder(fi, space.Name); err != nil {
			return nil, err
		}
	}
	if space.Quota != nil {
		if err := c.SetFolderQuota(spaceID, snapToKWQuota(space.Quota.QuotaMaxBytes)); err != nil {
			return nil, err
		}
	}
	updated, err := c.GetFolderByID(spaceID)
	if err != nil {
		return nil, err
	}
	updatedSpace := d.toStorageSpace(ctx, updated, d.personalFolderID(c))
	return &provider.UpdateStorageSpaceResponse{
		Status:       &rpc.Status{Code: rpc.Code_CODE_OK},
		StorageSpace: updatedSpace,
	}, nil
}

func (d *Driver) DeleteStorageSpace(ctx context.Context, req *provider.DeleteStorageSpaceRequest) (*storage.DeleteStorageSpaceResult, error) {
	_, spaceID, _, _ := storagespace.SplitID(req.GetId().GetOpaqueId())
	if spaceID == "" {
		return nil, errtypes.BadRequest("kiteworks: DeleteStorageSpace requires a space ID")
	}
	c := d.client(ctx)
	fi, err := c.GetFolderByID(spaceID)
	if err != nil {
		var ce *kwlib.ClientError
		if errors.As(err, &ce) && ce.StatusCode == http.StatusNotFound {
			return nil, errtypes.NotFound(spaceID)
		}
		return nil, err
	}
	_, purge := req.GetOpaque().GetMap()["purge"]
	var deleteErr error
	if purge {
		deleteErr = c.PermanentDeleteFolder(spaceID)
	} else {
		deleteErr = c.DeleteFolder(spaceID)
	}
	if deleteErr != nil {
		var ce *kwlib.ClientError
		if errors.As(deleteErr, &ce) && ce.StatusCode == http.StatusForbidden {
			return nil, errtypes.PermissionDenied(spaceID)
		}
		return nil, deleteErr
	}
	return &storage.DeleteStorageSpaceResult{SpaceName: fi.Name}, nil
}

func (d *Driver) CreateHome(_ context.Context) error {
	return errtypes.NotSupported("kiteworks: read-only driver")
}

func (d *Driver) GetHome(_ context.Context) (string, error) {
	return "", errtypes.NotSupported("kiteworks: read-only driver")
}
