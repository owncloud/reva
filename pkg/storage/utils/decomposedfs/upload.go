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

package decomposedfs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/google/uuid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata/prefixes"
	"github.com/pkg/errors"
	"github.com/rogpeppe/go-internal/lockedfile"
	tusd "github.com/tus/tusd/v2/pkg/handler"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/upload"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// MarkProcessing toggles a processing flag on the resource.
func (fs *Decomposedfs) MarkProcessing(ctx context.Context, ref *provider.Reference, processing bool, sessionID string) error {
	n, err := fs.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return err
	}
	if !n.Exists {
		return errtypes.NotFound(ref.String())
	}

	// Early lock, so MarkProcessing is atomic.
	f, err := lockedfile.OpenFile(fs.lu.MetadataBackend().LockfilePath(n.InternalPath()), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			appctx.GetLogger(ctx).Error().Err(cerr).Str("nodeid", n.ID).Msg("could not close mark-processing lock")
		}
	}()

	// Evict the node's in-process xattr cache so IsProcessing reads from disk while we hold the lock.
	n.ResetXattrsCache()

	if !processing {
		if !n.IsProcessing(ctx) {
			return nil
		}
		id, _ := n.ProcessingID(ctx)
		if id != sessionID {
			return nil // owned by a different session, do not clear
		}
		return n.RemoveXattr(ctx, prefixes.StatusPrefix, false)
	}

	if n.IsProcessing(ctx) {
		return errtypes.ResourceProcessing(ref.String())
	}
	return n.SetXattrsWithContext(ctx, node.Attributes{
		prefixes.StatusPrefix: []byte(node.ProcessingStatus + sessionID),
	}, false) // acquireLock=false, because outer lock already held
}

// CommitUpload writes the staged bytes from source to the resource at ref.
func (fs *Decomposedfs) CommitUpload(ctx context.Context, ref *provider.Reference, source storage.UploadSource) (*provider.ResourceInfo, error) {
	if source.Body == nil {
		return nil, errtypes.BadRequest("Decomposedfs: source body is nil")
	}
	defer source.Body.Close()
	n, err := fs.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !n.Exists {
		return nil, errtypes.NotFound(ref.String())
	}
	if len(source.Checksums.SHA1) == 0 || len(source.Checksums.MD5) == 0 || len(source.Checksums.Adler32) == 0 {
		return nil, errtypes.BadRequest("Decomposedfs: pre-computed checksums missing from source")
	}
	attrs := node.Attributes{
		prefixes.ChecksumPrefix + "sha1":    source.Checksums.SHA1,
		prefixes.ChecksumPrefix + "md5":     source.Checksums.MD5,
		prefixes.ChecksumPrefix + "adler32": source.Checksums.Adler32,
	}
	n.BlobID = uuid.New().String()
	n.Blobsize = source.Length

	attrs.SetString(prefixes.IDAttr, n.ID)
	attrs.SetInt64(prefixes.TypeAttr, int64(provider.ResourceType_RESOURCE_TYPE_FILE))
	attrs.SetString(prefixes.ParentidAttr, n.ParentID)
	attrs.SetString(prefixes.NameAttr, n.Name)
	attrs.SetString(prefixes.BlobIDAttr, n.BlobID)
	attrs.SetInt64(prefixes.BlobsizeAttr, n.Blobsize)

	mtime := time.Now()
	if mts := source.Metadata["mtime"]; mts != "" {
		parsed, err := utils.MTimeToTime(mts)
		if err != nil {
			return nil, errtypes.BadRequest("invalid mtime: " + mts)
		}
		mtime = parsed
	}

	if fs.um != nil {
		if gid, ok := ctx.Value(CtxKeySpaceGID).(uint32); ok {
			unscope, err := fs.um.ScopeUserByIds(-1, int(gid))
			if err != nil {
				return nil, errors.Wrap(err, "Decomposedfs: failed to scope user")
			}
			if unscope != nil {
				defer func() { _ = unscope() }()
			}
		}
	}

	n.SpaceRoot, err = node.ReadNode(ctx, fs.lu, n.SpaceID, n.SpaceID, false, nil, false)
	if err != nil {
		return nil, err
	}
	if err := n.CheckLock(ctx); err != nil {
		return nil, err
	}

	var (
		unlock   metadata.UnlockFunc
		sizeDiff int64
	)
	defer func() {
		if unlock == nil {
			return
		}
		if err := unlock(); err != nil {
			appctx.GetLogger(ctx).Error().Err(err).Str("nodeid", n.ID).Str("parentid", n.ParentID).Msg("could not close lock")
		}
	}()

	f, err := lockedfile.OpenFile(fs.lu.MetadataBackend().LockfilePath(n.InternalPath()), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to lock node for overwrite")
	}
	unlock = func() error { return f.Close() }

	old, err := node.ReadNode(ctx, fs.lu, n.SpaceID, n.ID, false, nil, false)
	if err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to read existing node")
	}
	if _, err := node.CheckQuota(ctx, n.SpaceRoot, old.BlobID != "", uint64(old.Blobsize), uint64(source.Length)); err != nil {
		return nil, err
	}

	oldNodeMtime, err := old.GetMTime(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to read old mtime")
	}

	if !fs.o.DisableVersioning && old.BlobID != "" {
		versionPath := fs.lu.InternalPath(n.SpaceID, n.ID+node.RevisionIDDelimiter+oldNodeMtime.UTC().Format(time.RFC3339Nano))

		revFile, err := os.OpenFile(versionPath, os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, errors.Wrap(err, "Decomposedfs: failed to create revision file")
			}
			// EEXIST: a revision archive at this mtime already exists from a
			// prior CommitUpload run. If the archive is byte-identical to the
			// live node, it is a leftover from an idempotent retry and can be
			// safely reset; otherwise we refuse rather than clobber history.
			if err := validateRevisionChecksums(ctx, fs.lu, old, versionPath); err != nil {
				return nil, errors.Wrap(err, "Decomposedfs: existing revision archive does not match current node")
			}
			bID, _, err := fs.lu.ReadBlobIDAndSizeAttr(ctx, versionPath, nil)
			if err != nil {
				return nil, errors.Wrap(err, "Decomposedfs: failed to read blob id of existing revision")
			}
			if err := fs.tp.DeleteBlob(&node.Node{BlobID: bID, SpaceID: n.SpaceID}); err != nil {
				return nil, errors.Wrap(err, "Decomposedfs: failed to delete stale revision blob")
			}
			revFile, err = os.Create(versionPath)
			if err != nil {
				return nil, errors.Wrap(err, "Decomposedfs: failed to truncate revision file")
			}
		}
		revFile.Close()

		if err := fs.lu.CopyMetadataWithSourceLock(ctx, n.InternalPath(), versionPath,
			func(name string, value []byte) ([]byte, bool) {
				return value, strings.HasPrefix(name, prefixes.ChecksumPrefix) ||
					name == prefixes.TypeAttr ||
					name == prefixes.BlobIDAttr ||
					name == prefixes.BlobsizeAttr ||
					name == prefixes.MTimeAttr
			}, f, true); err != nil {
			return nil, errors.Wrap(err, "Decomposedfs: failed to archive current revision")
		}

		if err := os.Chtimes(versionPath, oldNodeMtime, oldNodeMtime); err != nil {
			return nil, errors.Wrap(err, "Decomposedfs: failed to set revision mtime")
		}
	}
	sizeDiff = source.Length - old.Blobsize

	revisionNode := node.New(n.SpaceID, n.ID, n.ParentID, n.Name, n.Blobsize, n.BlobID,
		provider.ResourceType_RESOURCE_TYPE_FILE, nil, fs.lu)
	if err := fs.tp.WriteBlobFromReader(revisionNode, source.Body, source.Length); err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to write blob")
	}

	// The blob now exists in the blobstore but the node metadata does not yet
	// reference it. If any of the steps below fail we return an error without
	// persisting that reference, leaving the blob orphaned. Delete it on the
	// error path; cleared once the commit completes.
	committed := false
	defer func() {
		if committed {
			return
		}
		if derr := fs.tp.DeleteBlob(revisionNode); derr != nil {
			appctx.GetLogger(ctx).Error().Err(derr).Str("nodeid", n.ID).Str("blobid", n.BlobID).Msg("could not clean up orphaned blob after failed commit")
		}
	}()

	if err := fs.lu.TimeManager().OverrideMtime(ctx, n, &attrs, mtime); err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to set the mtime")
	}

	if err := n.SetXattrsWithContext(ctx, attrs, false); err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to write metadata")
	}
	// Durable commit point: the node metadata now references the new blob.
	// Past here the file is committed, so the orphaned-blob cleanup must no
	// longer run - a failure in the post-commit steps below leaves the file
	// intact and must not delete the referenced blob.
	committed = true

	if err := fs.tp.Propagate(ctx, n, sizeDiff); err != nil {
		return nil, errors.Wrap(err, "Decomposedfs: failed to propagate")
	}
	// etag is a best-effort, recomputable value; a failure here must not fail an
	// already-committed upload (matches the legacy Upload path).
	etag, _ := node.CalculateEtag(n.ID, mtime)

	return &provider.ResourceInfo{
		Id: &provider.ResourceId{
			StorageId: source.Metadata["providerID"],
			SpaceId:   n.SpaceID,
			OpaqueId:  n.ID,
		},
		Etag:  etag,
		Mtime: utils.TimeToTS(mtime),
	}, nil
}

// validateRevisionChecksums returns nil iff every checksum xattr (md5, sha1,
// adler32) on the live node n equals the same xattr on the archive at
// versionPath. Used to detect a leftover archive from an idempotent retry.
func validateRevisionChecksums(ctx context.Context, lu node.PathLookup, n *node.Node, versionPath string) error {
	for _, algo := range []string{"md5", "sha1", "adler32"} {
		key := prefixes.ChecksumPrefix + algo

		live, err := n.Xattr(ctx, key)
		if err != nil {
			return err
		}
		archived, err := lu.MetadataBackend().Get(ctx, versionPath, key)
		if err != nil {
			return err
		}
		if len(live) == 0 || len(archived) == 0 {
			return errors.New("checksum not found")
		}
		if string(live) != string(archived) {
			return errors.New("checksum mismatch on " + algo)
		}
	}
	return nil
}

// UseIn tells the tus upload middleware which extensions it supports.
func (fs *Decomposedfs) UseIn(composer *tusd.StoreComposer) {
	composer.UseCore(fs)
	composer.UseTerminater(fs)
	composer.UseConcater(fs)
	composer.UseLengthDeferrer(fs)
}

// To implement the core tus.io protocol as specified in https://tus.io/protocols/resumable-upload.html#core-protocol
// - the storage needs to implement NewUpload and GetUpload
// - the upload needs to implement the tusd.Upload interface: WriteChunk, GetInfo, GetReader and FinishUpload

// NewUpload returns a new tus Upload instance
func (fs *Decomposedfs) NewUpload(ctx context.Context, info tusd.FileInfo) (tusd.Upload, error) {
	return nil, fmt.Errorf("not implemented, use InitiateUpload on the CS3 API to start a new upload")
}

// GetUpload returns the Upload for the given upload id
func (fs *Decomposedfs) GetUpload(ctx context.Context, id string) (tusd.Upload, error) {
	var ul tusd.Upload
	var err error
	_ = fs.um.RunInBaseScope(func() error {
		ul, err = fs.sessionStore.Get(ctx, id)
		return nil
	})
	return ul, err
}

// ListUploadSessions returns the upload sessions for the given filter
func (fs *Decomposedfs) ListUploadSessions(ctx context.Context, filter storage.UploadSessionFilter) ([]storage.UploadSession, error) {
	var sessions []*upload.OcisSession
	if filter.ID != nil && *filter.ID != "" {
		session, err := fs.sessionStore.Get(ctx, *filter.ID)
		if err != nil {
			return nil, err
		}
		sessions = []*upload.OcisSession{session}
	} else {
		var err error
		sessions, err = fs.sessionStore.List(ctx)
		if err != nil {
			return nil, err
		}
	}
	filteredSessions := []storage.UploadSession{}
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
		filteredSessions = append(filteredSessions, session)
	}
	return filteredSessions, nil
}

// AsTerminatableUpload returns a TerminatableUpload
// To implement the termination extension as specified in https://tus.io/protocols/resumable-upload.html#termination
// the storage needs to implement AsTerminatableUpload
func (fs *Decomposedfs) AsTerminatableUpload(up tusd.Upload) tusd.TerminatableUpload {
	return up.(*upload.OcisSession)
}

// AsLengthDeclarableUpload returns a LengthDeclarableUpload
// To implement the creation-defer-length extension as specified in https://tus.io/protocols/resumable-upload.html#creation
// the storage needs to implement AsLengthDeclarableUpload
func (fs *Decomposedfs) AsLengthDeclarableUpload(up tusd.Upload) tusd.LengthDeclarableUpload {
	return up.(*upload.OcisSession)
}

// AsConcatableUpload returns a ConcatableUpload
// To implement the concatenation extension as specified in https://tus.io/protocols/resumable-upload.html#concatenation
// the storage needs to implement AsConcatableUpload
func (fs *Decomposedfs) AsConcatableUpload(up tusd.Upload) tusd.ConcatableUpload {
	return up.(*upload.OcisSession)
}
