package upload

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	tusd "github.com/tus/tusd/v2/pkg/handler"

	"github.com/owncloud/reva/v2/pkg/appctx"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/chunking"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// Coordinator owns the upload lifecycle: initiation, data transfer and listing.
type Coordinator interface {
	// InitiateUpload returns the protocols and ids that bytes can be appended to.
	InitiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error)
	// GetUpload returns the session with the given id as a tusd upload.
	GetUpload(ctx context.Context, id string) (tusd.Upload, error)
	// UseIn registers the coordinator as the tusd data store.
	UseIn(composer *tusd.StoreComposer)
	// ListUploadSessions returns the upload sessions matching the given filter.
	ListUploadSessions(ctx context.Context, filter storage.UploadSessionFilter) ([]storage.UploadSession, error)
	// Upload writes the whole body of a non-resumable (PUT) upload and finishes it.
	Upload(ctx context.Context, req storage.UploadRequest, uff storage.UploadFinishedFunc) (*provider.ResourceInfo, error)
}

// coordinator is the concrete implementation of Coordinator.
type coordinator struct {
	fs           storage.FS
	store        SessionStore
	chunkHandler *chunking.ChunkHandler
	pub          events.Publisher
	// async defers the commit to postprocessing. Only StartPostprocessing sets it.
	async bool
}

// NewCoordinator constructs a coordinator backed by the given driver and store.
func NewCoordinator(fs storage.FS, store SessionStore, chunkFolder string, pub events.Publisher) *coordinator {
	c := &coordinator{fs: fs, store: store, pub: pub}
	if chunkFolder != "" {
		c.chunkHandler = chunking.NewChunkHandler(chunkFolder)
	}
	return c
}

// uploadTarget is what an upload needs to know about its destination
type uploadTarget struct {
	// exists distinguishes an overwrite from a new file.
	exists bool
	// chunkName is set for legacy chunking-v1 uploads only.
	chunkName string

	nodeID     string
	spaceID    string
	parentID   string
	dir        string
	name       string
	spaceOwner *user.UserId
}

// resolveTarget locates the file an upload writes to, and rejects an upload that
// may not proceed.
func (c *coordinator) resolveTarget(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (*uploadTarget, error) {
	t := &uploadTarget{}
	if chunking.IsChunked(ref.GetPath()) { // check legacy chunking v1
		var err error
		ref, t.chunkName, err = rewriteChunkedRef(ref)
		if err != nil {
			return nil, err
		}
	}

	// TODO: GetMD reports a file hidden by a deny-grant as NotFound, so the upload
	// fails late with 409 instead of 403. Needs a permission-free resolve.
	existing, err := c.fs.GetMD(ctx, ref, []string{}, []string{})
	switch err.(type) {
	case nil:
		t.exists = true
	case errtypes.IsNotFound:
		t.exists = false
	default:
		return nil, err
	}

	if err := c.checkQuota(ctx, ref, uploadLength, existing); err != nil {
		return nil, err
	}

	if t.exists {
		if err := c.describeExisting(ctx, ref, existing, metadata, t); err != nil {
			return nil, err
		}
	} else if err := c.describeNew(ctx, ref, t); err != nil {
		return nil, err
	}

	if t.name == "" {
		return nil, errtypes.BadRequest("coordinator: missing filename in ref")
	}
	if t.dir == "" {
		return nil, errtypes.BadRequest("coordinator: could not determine upload directory")
	}
	return t, nil
}

// checkQuota rejects an upload that would not fit in the space, counting only
// the bytes it adds.
func (c *coordinator) checkQuota(ctx context.Context, ref *provider.Reference, uploadLength int64, existing *provider.ResourceInfo) error {
	if uploadLength < 0 {
		return nil
	}
	spaceRef := &provider.Reference{ResourceId: &provider.ResourceId{
		StorageId: ref.GetResourceId().GetStorageId(),
		SpaceId:   ref.GetResourceId().GetSpaceId(),
	}}
	_, _, remaining, err := c.fs.GetQuota(ctx, spaceRef)
	if err != nil {
		return nil
	}

	netRequired := uint64(uploadLength)
	if existingSize := existing.GetSize(); existingSize < netRequired {
		netRequired -= existingSize
	} else {
		netRequired = 0
	}
	if remaining < netRequired {
		return errtypes.InsufficientStorage("quota exceeded")
	}
	return nil
}

// describeExisting fills in the target from the file being overwritten.
func (c *coordinator) describeExisting(ctx context.Context, ref *provider.Reference, existing *provider.ResourceInfo, metadata map[string]string, t *uploadTarget) error {
	if !existing.GetPermissionSet().GetInitiateFileUpload() {
		return errtypes.PermissionDenied(ref.GetPath())
	}
	if existing.GetType() == provider.ResourceType_RESOURCE_TYPE_CONTAINER {
		return errtypes.PreconditionFailed("resource is not a file")
	}

	t.nodeID = existing.GetId().GetOpaqueId()
	t.spaceID = existing.GetId().GetSpaceId()
	t.parentID = existing.GetParentId().GetOpaqueId()
	t.name = existing.GetName()
	// TODO: no manager fallback, so this stays nil on project drives.
	t.spaceOwner = existing.GetOwner()

	// GetMD returns only the basename for id-based refs, so ask for the full path.
	relPath := existing.GetPath()
	if utils.IsRelativeReference(ref) {
		if full, err := c.fs.GetPathByID(ctx, existing.GetId()); err == nil {
			relPath = full
		}
	}
	t.dir = filepath.Dir(relPath)

	// Lock before precondition, the order main checks them in (upload.go:293-302).
	if err := c.checkLock(ctx, ref); err != nil {
		return err
	}
	if metadata["if-none-match"] == "*" {
		return errtypes.Aborted(fmt.Sprintf("parent %s already has a child %s, id %s", t.parentID, t.name, t.nodeID))
	}
	return nil
}

// checkLock rejects an upload that conflicts with a lock held on the file.
func (c *coordinator) checkLock(ctx context.Context, ref *provider.Reference) error {
	// An unlocked file and a driver without locks both error, so only a lock we
	// actually read counts as one.
	diskLock, err := c.fs.GetLock(ctx, ref)
	if err != nil {
		diskLock = nil
	}
	contextLockID, _ := ctxpkg.ContextGetLockID(ctx)

	switch {
	case diskLock == nil && contextLockID != "":
		return errtypes.Aborted("not locked")
	case diskLock == nil:
		return nil
	case contextLockID == "":
		return errtypes.Locked(diskLock.LockId)
	case contextLockID != diskLock.LockId:
		return errtypes.Aborted("mismatching lock")
	}
	return nil
}

// describeNew fills in the target for a file that does not exist yet, from its
// parent directory.
func (c *coordinator) describeNew(ctx context.Context, ref *provider.Reference, t *uploadTarget) error {
	t.spaceID = ref.GetResourceId().GetSpaceId()
	t.dir = filepath.Dir(ref.GetPath())
	t.name = filepath.Base(ref.GetPath())

	parentRef := &provider.Reference{ResourceId: ref.GetResourceId(), Path: t.dir}
	parentMD, err := c.fs.GetMD(ctx, parentRef, []string{}, []string{})
	switch err.(type) {
	case nil:
	case errtypes.IsNotFound:
		return c.missingParentError(ctx, ref, t.dir, err)
	default:
		return err
	}

	if !parentMD.GetPermissionSet().GetInitiateFileUpload() {
		return errtypes.PermissionDenied(ref.GetPath())
	}
	t.parentID = parentMD.GetId().GetOpaqueId()
	t.spaceID = parentMD.GetId().GetSpaceId()

	// An id-based ref yields a relative dir ("."), so ask for the parent's full path.
	if utils.IsRelativeReference(ref) {
		if parentPath, pErr := c.fs.GetPathByID(ctx, parentMD.GetId()); pErr == nil {
			t.dir = parentPath
		}
	}
	return nil
}

// missingParentError tells a missing directory apart from one that is only
// invisible to the caller: a visible ancestor means it is missing.
func (c *coordinator) missingParentError(ctx context.Context, ref *provider.Reference, dir string, notFound error) error {
	for ancestor := dir; ancestor != "." && ancestor != "/"; {
		ancestor = filepath.Dir(ancestor)
		ancestorRef := &provider.Reference{ResourceId: ref.GetResourceId(), Path: ancestor}
		if _, err := c.fs.GetMD(ctx, ancestorRef, []string{}, []string{}); err == nil {
			return errtypes.PreconditionFailed(notFound.Error())
		}
	}
	return errtypes.PermissionDenied(ref.GetPath())
}

// rewriteChunkedRef turns a legacy chunking-v1 path into a reference to the real
// target file, plus the chunk name.
func rewriteChunkedRef(ref *provider.Reference) (*provider.Reference, string, error) {
	ci, err := chunking.GetChunkBLOBInfo(ref.GetPath())
	if err != nil {
		return nil, "", errtypes.BadRequest(err.Error())
	}
	return &provider.Reference{ResourceId: ref.ResourceId, Path: ci.Path}, filepath.Base(ref.GetPath()), nil
}

// ListUploadSessions returns the upload sessions matching the given filter.
func (c *coordinator) ListUploadSessions(ctx context.Context, filter storage.UploadSessionFilter) ([]storage.UploadSession, error) {
	// Only the driver can resolve a session's node, so refuse rather than
	// silently report every session as a match.
	if filter.Orphaned != nil {
		return nil, errtypes.NotSupported("coordinator: the orphaned filter is not supported")
	}

	var sessions []Session
	if filter.ID != nil && *filter.ID != "" {
		session, err := c.store.Get(ctx, *filter.ID)
		if err != nil {
			return nil, err
		}
		sessions = []Session{session}
	} else {
		var err error
		sessions, err = c.store.List(ctx)
		if err != nil {
			return nil, err
		}
	}

	filtered := []storage.UploadSession{}
	now := time.Now()
	for _, session := range sessions {
		if filter.Processing != nil && *filter.Processing != session.IsProcessing() {
			continue
		}
		if filter.Expired != nil {
			if *filter.Expired {
				if now.Before(session.Expires()) {
					continue
				}
			} else {
				if now.After(session.Expires()) {
					continue
				}
			}
		}
		if filter.HasVirus != nil {
			sr, _ := session.ScanData()
			infected := sr != ""
			if *filter.HasVirus != infected {
				continue
			}
		}
		filtered = append(filtered, session)
	}
	return filtered, nil
}

// uploadRef builds the reference upload events carry: the space root, plus the
// file's path within it.
func (c *coordinator) uploadRef(session Session) *provider.Reference {
	return &provider.Reference{
		ResourceId: &provider.ResourceId{
			StorageId: session.ProviderID(),
			SpaceId:   session.SpaceID(),
			OpaqueId:  session.SpaceID(),
		},
		Path: utils.MakeRelativePath(filepath.Join(session.Dir(), session.Filename())),
	}
}

// impersonatingUser returns the real actor behind a borrowed identity, which
// public link and OCM auth record in the request user's opaque.
func impersonatingUser(ctx context.Context) *user.User {
	u, ok := ctxpkg.ContextGetUser(ctx)
	if !ok || !utils.ExistsInOpaque(u.GetOpaque(), "impersonating-user") {
		return nil
	}
	impersonating := &user.User{}
	if err := utils.ReadJSONFromOpaque(u.GetOpaque(), "impersonating-user", impersonating); err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Msg("could not read impersonating user")
		return nil
	}
	return impersonating
}
