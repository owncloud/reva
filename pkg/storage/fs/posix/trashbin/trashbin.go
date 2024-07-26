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

package trashbin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/cs3org/reva/v2/pkg/storage"
	"github.com/cs3org/reva/v2/pkg/storage/fs/posix/lookup"
	"github.com/cs3org/reva/v2/pkg/storage/fs/posix/options"
	"github.com/cs3org/reva/v2/pkg/storage/utils/decomposedfs/metadata/prefixes"
	"github.com/cs3org/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/cs3org/reva/v2/pkg/utils"
	"github.com/google/uuid"
)

type Trashbin struct {
	fs storage.FS
	o  *options.Options
	lu *lookup.Lookup
}

// New returns a new Trashbin
func New(o *options.Options, lu *lookup.Lookup) (*Trashbin, error) {
	return &Trashbin{
		o:  o,
		lu: lu,
	}, nil
}

func (tb *Trashbin) writeInfoFile(trashPath, id, path string) error {
	c := "[Trash Info]"
	c += "\nPath=" + path
	c += "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05")

	return os.WriteFile(filepath.Join(trashPath, "info", id+".trashinfo"), []byte(c), 0644)
}

func (tb *Trashbin) readInfoFile(trashPath, id string) (string, *typesv1beta1.Timestamp, error) {
	c, err := os.ReadFile(filepath.Join(trashPath, "info", id+".trashinfo"))
	if err != nil {
		return "", nil, err
	}

	var (
		path string
		ts   *typesv1beta1.Timestamp
	)

	for _, line := range strings.Split(string(c), "\n") {
		if strings.HasPrefix(line, "DeletionDate=") {
			t, err := time.Parse("2006-01-02T15:04:05", strings.TrimPrefix(line, "DeletionDate="))
			if err != nil {
				return "", nil, err
			}
			ts = utils.TimeToTS(t)
		}
		if strings.HasPrefix(line, "Path=") {
			path = strings.TrimPrefix(line, "Path=")
		}
	}

	return path, ts, nil
}

// Setup the trashbin
func (tb *Trashbin) Setup(fs storage.FS) error {
	if tb.fs != nil {
		return nil
	}

	tb.fs = fs
	return nil
}

func trashRootForNode(n *node.Node) string {
	return filepath.Join(n.SpaceRoot.InternalPath(), ".Trash")
}

func (tb *Trashbin) MoveToTrash(n *node.Node, path string) error {
	key := uuid.New().String()
	trashPath := trashRootForNode(n)

	err := os.MkdirAll(filepath.Join(trashPath, "info"), 0755)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Join(trashPath, "files"), 0755)
	if err != nil {
		return err
	}

	relPath := strings.TrimPrefix(path, n.SpaceRoot.InternalPath())
	relPath = strings.TrimPrefix(relPath, "/")
	err = tb.writeInfoFile(trashPath, key, relPath)
	if err != nil {
		return err
	}

	return os.Rename(path, filepath.Join(trashPath, "files", key+".trashitem"))
}

// ListRecycle returns the list of available recycle items
// ref -> the space (= resourceid), key -> deleted node id, relativePath = relative to key
func (tb *Trashbin) ListRecycle(ctx context.Context, ref *provider.Reference, key, relativePath string) ([]*provider.RecycleItem, error) {
	n, err := tb.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return nil, err
	}

	trashRoot := trashRootForNode(n)
	base := filepath.Join(trashRoot, "files")
	if key != "" {
		base = filepath.Join(base, key+".trashitem", relativePath)
	}

	entries, err := os.ReadDir(filepath.Clean(base))
	if err != nil {
		return nil, err
	}

	items := []*provider.RecycleItem{}
	for _, entry := range entries {
		key := strings.TrimSuffix(entry.Name(), ".trashitem")
		originalPath, ts, err := tb.readInfoFile(trashRoot, key)
		if err != nil {
			continue
		}

		fi, err := entry.Info()
		if err != nil {
			continue
		}

		item := &provider.RecycleItem{
			Key:  key,
			Size: uint64(fi.Size()),
			Ref: &provider.Reference{
				ResourceId: &provider.ResourceId{
					SpaceId:  ref.GetResourceId().GetSpaceId(),
					OpaqueId: ref.GetResourceId().GetSpaceId(),
				},
				Path: originalPath,
			},
			DeletionTime: ts,
		}
		if entry.IsDir() {
			item.Type = provider.ResourceType_RESOURCE_TYPE_CONTAINER
		} else {
			item.Type = provider.ResourceType_RESOURCE_TYPE_FILE
		}

		items = append(items, item)
	}

	return items, nil
}

// RestoreRecycleItem restores the specified item
func (tb *Trashbin) RestoreRecycleItem(ctx context.Context, ref *provider.Reference, key, relativePath string, restoreRef *provider.Reference) error {
	n, err := tb.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return err
	}

	trashRoot := trashRootForNode(n)
	trashPath := filepath.Clean(filepath.Join(trashRoot, "files", key+".trashitem", relativePath))
	originalPath, _, err := tb.readInfoFile(trashRoot, key)
	if err != nil {
		return err
	}
	restorePath := filepath.Join(n.SpaceRoot.InternalPath(), originalPath)

	id, err := tb.lu.MetadataBackend().Get(ctx, trashPath, prefixes.IDAttr)
	if err != nil {
		return err
	}

	err = os.Rename(trashPath, restorePath)
	if err != nil {
		return err
	}

	tb.lu.CacheID(ctx, n.SpaceID, string(id), restorePath)

	// cleanup trash info
	return os.Remove(filepath.Join(trashRoot, "info", key+".trashinfo"))
}

// PurgeRecycleItem purges the specified item, all its children and all their revisions
func (tb *Trashbin) PurgeRecycleItem(ctx context.Context, ref *provider.Reference, key, relativePath string) error {
	n, err := tb.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return err
	}

	trashRoot := trashRootForNode(n)
	err = os.Remove(filepath.Clean(filepath.Join(trashRoot, "files", key+".trashitem", relativePath)))
	if err != nil {
		return err
	}

	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || cleanPath == "/" {
		return os.Remove(filepath.Join(trashRoot, "info", key+".trashinfo"))
	}
	return nil
}

// EmptyRecycle empties the trash
func (tb *Trashbin) EmptyRecycle(ctx context.Context, ref *provider.Reference) error {
	n, err := tb.lu.NodeFromResource(ctx, ref)
	if err != nil {
		return err
	}

	trashRoot := trashRootForNode(n)
	err = os.RemoveAll(filepath.Clean(filepath.Join(trashRoot, "files")))
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Clean(filepath.Join(trashRoot, "info")))
}
