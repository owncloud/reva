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

// Package upload provides the driver-agnostic upload coordinator.
package upload

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/rhttp/datatx/metrics"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/chunking"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// Coordinator owns the upload lifecycle.
type Coordinator interface {
	// InitiateUpload returns a list of protocols with urls that can be used to append bytes to a new upload session.
	InitiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error)
}

// coordinator is the concrete implementation of Coordinator.
type coordinator struct {
	fs    storage.FS
	store SessionStore
}

// NewCoordinator constructs a coordinator backed by the given storage driver.
func NewCoordinator(fs storage.FS) *coordinator {
	return &coordinator{fs: fs}
}

// InitiateUpload returns a list of protocols with urls that can be used to append bytes to a new upload session.
//
// For now this delegates straight to the underlying storage driver, preserving
// existing behaviour. It lets us wire the coordinator into the storageprovider
// and exercise the seam end-to-end before porting the driver-agnostic upload
// logic into the coordinator itself.
func (c *coordinator) InitiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error) {
	return c.fs.InitiateUpload(ctx, ref, uploadLength, metadata)
}

// initiateUpload is the driver-agnostic port of decomposedfs.InitiateUpload.
//
// It is DEAD CODE for now: the exported pass-through above still serves all
// traffic. This method is copied verbatim from the colleague's OCISDEV-900 branch
// so we can validate it block-by-block against main before wiring it in. Two known
// divergences from main will be fixed here (not inherited):
//   - F1: mint the new-file NodeId here (main's behaviour) so OC-FileId is populated.
//   - quota fails open on GetQuota error; main aborts.
//
// The zero-length finish branch is stubbed until finishUpload (data path) is ported.
func (c *coordinator) initiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error) {
	var chunkName string
	if chunking.IsChunked(ref.GetPath()) { // check legacy chunking v1
		var rerr error
		ref, chunkName, rerr = rewriteChunkedRef(ref)
		if rerr != nil {
			return nil, rerr
		}
	}

	existing, err := c.fs.GetMD(ctx, ref, []string{}, []string{})
	var nodeExists bool
	switch err.(type) {
	case nil:
		nodeExists = true
	case errtypes.IsNotFound:
		nodeExists = false
	default:
		return nil, err
	}

	var nodeID, spaceID, parentID, dir, nodeName string
	var spaceOwner *user.UserId

	// check quota
	if uploadLength >= 0 {
		spaceRef := &provider.Reference{ResourceId: &provider.ResourceId{
			StorageId: ref.GetResourceId().GetStorageId(),
			SpaceId:   ref.GetResourceId().GetSpaceId(),
		}}
		if _, _, remaining, qErr := c.fs.GetQuota(ctx, spaceRef); qErr == nil {
			var existingSize uint64
			if nodeExists {
				existingSize = existing.GetSize()
			}
			netRequired := uint64(uploadLength)
			if existingSize < netRequired {
				netRequired -= existingSize
			} else {
				netRequired = 0
			}
			if remaining < netRequired {
				return nil, errtypes.InsufficientStorage("quota exceeded")
			}
		}
	}

	if nodeExists {
		nodeID = existing.GetId().GetOpaqueId()
		spaceID = existing.GetId().GetSpaceId()
		parentID = existing.GetParentId().GetOpaqueId()
		// GetMD returns only the basename for relative (id-based) refs, so
		// filepath.Dir would yield "." here. Reconstruct the space-relative
		// path via the public FS interface — mirrors main's fs.lu.Path.
		// Best-effort: on error keep the basename rather than failing an
		// upload main would allow.
		relPath := existing.GetPath()
		if utils.IsRelativeReference(ref) {
			if full, pErr := c.fs.GetPathByID(ctx, existing.GetId()); pErr == nil {
				relPath = full
			}
		}
		dir = filepath.Dir(relPath)
		nodeName = existing.GetName()
		spaceOwner = existing.GetOwner()

		diskLock, _ := c.fs.GetLock(ctx, ref)
		contextLockID, _ := ctxpkg.ContextGetLockID(ctx)
		if diskLock != nil {
			switch contextLockID {
			case "":
				return nil, errtypes.Locked(diskLock.LockId)
			case diskLock.LockId:
				// ok
			default:
				return nil, errtypes.Aborted("mismatching lock")
			}
		} else if contextLockID != "" {
			return nil, errtypes.Aborted("not locked")
		}
	} else {
		spaceID = ref.GetResourceId().GetSpaceId()
		dir = filepath.Dir(ref.GetPath())
		nodeName = filepath.Base(ref.GetPath())
	}

	if nodeExists {
		if !existing.GetPermissionSet().GetInitiateFileUpload() {
			return nil, errtypes.PermissionDenied(ref.GetPath())
		}
		if existing.GetType() == provider.ResourceType_RESOURCE_TYPE_CONTAINER {
			return nil, errtypes.PreconditionFailed("resource is not a file")
		}
		if metadata["if-none-match"] == "*" {
			return nil, errtypes.Aborted(fmt.Sprintf("parent %s already has a child %s, id %s", parentID, nodeName, nodeID))
		}
	} else {
		parentRef := &provider.Reference{
			ResourceId: ref.GetResourceId(),
			Path:       dir,
		}
		parentMD, pErr := c.fs.GetMD(ctx, parentRef, []string{}, []string{})
		switch pErr.(type) {
		case nil:
		case errtypes.IsNotFound:
			// RFC 4918: missing intermediate dir → 409, no permission → 404.
			// GetMD returns NotFound for both (hides resources from unauthorized callers).
			// Walk up the path: if an ancestor is visible, the dir is truly missing (409).
			// If nothing is visible up to the root, caller has no access (404).
			ancestor := dir
			permDenied := true
			for ancestor != "." && ancestor != "/" {
				ancestor = filepath.Dir(ancestor)
				ancestorRef := &provider.Reference{ResourceId: ref.GetResourceId(), Path: ancestor}
				if _, aErr := c.fs.GetMD(ctx, ancestorRef, []string{}, []string{}); aErr == nil {
					permDenied = false
					break
				}
			}
			if permDenied {
				return nil, errtypes.PermissionDenied(ref.GetPath())
			}
			return nil, errtypes.PreconditionFailed(pErr.Error())
		default:
			return nil, pErr
		}
		if !parentMD.GetPermissionSet().GetInitiateFileUpload() {
			return nil, errtypes.PermissionDenied(ref.GetPath())
		}
		parentID = parentMD.GetId().GetOpaqueId()
		spaceID = parentMD.GetId().GetSpaceId()
	}

	if nodeName == "" {
		return nil, errtypes.BadRequest("coordinator: missing filename in ref")
	}
	if dir == "" {
		return nil, errtypes.BadRequest("coordinator: could not determine upload directory")
	}

	session := c.store.New(ctx)
	session.SetMetadata("filename", nodeName)
	session.SetStorageValue("NodeName", nodeName)
	session.SetMetadata("dir", dir)
	session.SetStorageValue("Dir", dir)
	session.SetStorageValue("SpaceRoot", spaceID)
	if nodeExists {
		session.SetStorageValue("NodeId", nodeID)
		session.SetStorageValue("NodeExists", "true")
	}
	session.SetStorageValue("NodeParentId", parentID)
	if spaceOwner != nil {
		session.SetStorageValue("SpaceOwnerOrManager", spaceOwner.GetOpaqueId())
		session.SetStorageValue("SpaceOwnerIdp", spaceOwner.GetIdp())
		session.SetStorageValue("SpaceOwnerType", utils.UserTypeToString(spaceOwner.GetType()))
	}

	usr := ctxpkg.ContextMustGetUser(ctx)
	session.SetExecutant(usr)

	lockID, _ := ctxpkg.ContextGetLockID(ctx)
	session.SetMetadata("lockid", lockID)

	iid, _ := ctxpkg.ContextGetInitiator(ctx)
	session.SetMetadata("initiatorid", iid)

	session.SetSize(uploadLength)

	var mtimeSet bool
	if metadata != nil {
		session.SetMetadata("providerID", metadata["providerID"])
		if v, ok := metadata["mtime"]; ok && v != "null" {
			session.SetMetadata("mtime", v)
			mtimeSet = true
		}
		if v, ok := metadata["expires"]; ok && v != "null" {
			session.SetMetadata("expires", v)
		}
		if _, ok := metadata["sizedeferred"]; ok {
			session.SetSizeIsDeferred(true)
		}
		if checksum, ok := metadata["checksum"]; ok {
			parts := strings.SplitN(checksum, " ", 2)
			if len(parts) != 2 {
				return nil, errtypes.BadRequest("invalid checksum format. must be '[algorithm] [checksum]'")
			}
			switch parts[0] {
			case "sha1", "md5", "adler32":
				session.SetMetadata("checksum", checksum)
			default:
				return nil, errtypes.BadRequest("unsupported checksum algorithm: " + parts[0])
			}
		}
		if v := metadata["if-match"]; v != "" {
			session.SetMetadata("if-match", v)
		}
		if v := metadata["if-none-match"]; v != "" {
			session.SetMetadata("if-none-match", v)
		}
		if v := metadata["if-unmodified-since"]; v != "" {
			session.SetMetadata("if-unmodified-since", v)
		}
	}

	if !mtimeSet {
		session.SetMetadata("mtime", utils.TimeToOCMtime(time.Now()))
	}
	if chunkName != "" { // check legacy chunking v1
		session.SetStorageValue("Chunk", chunkName)
	}

	if err := session.TouchBin(); err != nil {
		return nil, fmt.Errorf("coordinator: could not create bin file: %w", err)
	}
	if err := session.Persist(ctx); err != nil {
		session.Cleanup(true, true)
		return nil, fmt.Errorf("coordinator: could not persist session: %w", err)
	}

	metrics.UploadSessionsInitiated.Inc()

	if uploadLength == 0 {
		// Zero-length uploads complete immediately without postprocessing.
		// TODO: port finishUpload (data path) in its own step; until then this
		// branch is unreachable because initiateUpload is not wired in.
		return nil, errtypes.NotSupported("coordinator: zero-length finish not yet ported")
	}

	return map[string]string{
		"simple": session.ID(),
		"tus":    session.ID(),
	}, nil
}

// rewriteChunkedRef parses a legacy chunking-v1 path, returning a reference to the
// real target file plus the original chunk name.
func rewriteChunkedRef(ref *provider.Reference) (*provider.Reference, string, error) {
	ci, err := chunking.GetChunkBLOBInfo(ref.GetPath())
	if err != nil {
		return nil, "", errtypes.BadRequest(err.Error())
	}
	return &provider.Reference{ResourceId: ref.ResourceId, Path: ci.Path}, filepath.Base(ref.GetPath()), nil
}
