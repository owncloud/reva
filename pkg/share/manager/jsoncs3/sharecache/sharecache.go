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

package sharecache

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/docstore"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// name is the Tracer name used to identify this instrumentation library.
const tracerName = "sharecache"

// Cache holds the list of share ids for users and groups. It keeps one document
// for each user or group, in a docstore. The storage is sharded by user or
// group.
//
// A document that List returns is read-only. A caller must not change it.
type Cache struct {
	store *docstore.Store[UserShareCache]

	namespace string
	filename  string
}

// UserShareCache holds the space/share map for one user
type UserShareCache struct {
	UserShares map[string]*SpaceShareIDs

	Etag string
}

// SpaceShareIDs holds the unique list of share ids for a space
type SpaceShareIDs struct {
	IDs map[string]struct{}
}

var codec = docstore.Codec[UserShareCache]{
	New: func() *UserShareCache {
		return &UserShareCache{UserShares: map[string]*SpaceShareIDs{}}
	},
	Clone: func(u *UserShareCache) *UserShareCache {
		c := &UserShareCache{UserShares: make(map[string]*SpaceShareIDs, len(u.UserShares)), Etag: u.Etag}
		for ssid, ids := range u.UserShares {
			cp := &SpaceShareIDs{IDs: make(map[string]struct{}, len(ids.IDs))}
			for id := range ids.IDs {
				cp.IDs[id] = struct{}{}
			}
			c.UserShares[ssid] = cp
		}
		return c
	},
	Decode: func(data []byte) (*UserShareCache, error) {
		u := &UserShareCache{}
		if err := json.Unmarshal(data, u); err != nil {
			return nil, err
		}
		if u.UserShares == nil {
			u.UserShares = map[string]*SpaceShareIDs{}
		}
		for ssid, ids := range u.UserShares {
			if ids == nil {
				ids = &SpaceShareIDs{}
				u.UserShares[ssid] = ids
			}
			if ids.IDs == nil {
				ids.IDs = map[string]struct{}{}
			}
		}
		return u, nil
	},
	Encode: func(u *UserShareCache, etag string) ([]byte, error) {
		c := *u
		c.Etag = etag
		return json.Marshal(&c)
	},
	SetEtag: func(u *UserShareCache, etag string) { u.Etag = etag },
}

// New returns a new Cache instance. The ttl is not used.
func New(s metadata.Storage, namespace, filename string, _ time.Duration) Cache {
	return Cache{
		store: docstore.New(s, codec, docstore.Options{
			Tracer: func(ctx context.Context) trace.Tracer { return appctx.GetTracerProvider(ctx).Tracer(tracerName) },
			PathAttrs: func(p string) []attribute.KeyValue {
				return []attribute.KeyValue{attribute.String("cs3.userid", path.Base(path.Dir(p)))}
			},
		}),
		namespace: namespace,
		filename:  filename,
	}
}

func (c *Cache) userCreatedPath(userid string) string {
	return filepath.Join("/", c.namespace, userid, c.filename)
}

func ssidOf(shareID string) string {
	storageid, spaceid, _ := shareid.Decode(shareID)
	return storageid + shareid.IDDelimiter + spaceid
}

// Add adds a share to the cache
func (c *Cache) Add(ctx context.Context, userid, shareID string) error {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "Add")
	defer span.End()
	span.SetAttributes(attribute.String("cs3.userid", userid), attribute.String("cs3.shareid", shareID))

	ssid := ssidOf(shareID)
	err := c.store.Update(ctx, c.userCreatedPath(userid), func(d *UserShareCache) (bool, error) {
		ids := d.UserShares[ssid]
		if ids == nil {
			ids = &SpaceShareIDs{IDs: map[string]struct{}{}}
			d.UserShares[ssid] = ids
		}
		if _, ok := ids.IDs[shareID]; ok {
			return false, nil
		}
		ids.IDs[shareID] = struct{}{}
		return true, nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		appctx.GetLogger(ctx).Error().Err(err).Str("hostname", os.Getenv("HOSTNAME")).
			Str("userID", userid).Str("shareID", shareID).Msg("persisting added share failed")
		return err
	}
	span.SetStatus(codes.Ok, "")
	return nil
}

// Remove removes a share for the given user
func (c *Cache) Remove(ctx context.Context, userid, shareID string) error {
	return c.RemoveMany(ctx, userid, []string{shareID})
}

// RemoveMany removes the given shares of one user or group with one upload.
// The shares can be in different spaces. It does not upload when none of the
// shares is in the document.
func (c *Cache) RemoveMany(ctx context.Context, userid string, shareIDs []string) error {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "Remove")
	defer span.End()
	span.SetAttributes(attribute.String("cs3.userid", userid), attribute.Int("cs3.sharecount", len(shareIDs)))

	err := c.store.Update(ctx, c.userCreatedPath(userid), func(d *UserShareCache) (bool, error) {
		removed := false
		for _, shareID := range shareIDs {
			ssid := ssidOf(shareID)
			ids := d.UserShares[ssid]
			if ids == nil {
				continue
			}
			if _, ok := ids.IDs[shareID]; !ok {
				continue
			}
			delete(ids.IDs, shareID)
			// An empty space entry makes each listing read the space file again.
			if len(ids.IDs) == 0 {
				delete(d.UserShares, ssid)
			}
			removed = true
		}
		return removed, nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		appctx.GetLogger(ctx).Error().Err(err).Str("hostname", os.Getenv("HOSTNAME")).
			Str("userID", userid).Int("shareCount", len(shareIDs)).Msg("persisting removed share failed")
		return err
	}
	span.SetStatus(codes.Ok, "")
	return nil
}

// List returns the list of spaces and shares for the given user or group. The
// result is read-only: a caller must not change it.
func (c *Cache) List(ctx context.Context, userid string) (map[string]SpaceShareIDs, error) {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "List")
	defer span.End()
	span.SetAttributes(attribute.String("cs3.userid", userid))

	p := c.userCreatedPath(userid)
	e, err := c.store.Get(ctx, p)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if e.Etag == "" {
		// No file exists yet. Create the empty file, so the next sync gets a
		// NotModified instead of another NotFound. Only a listing does this.
		// Add writes the file anyway, and RemoveMany does not create it,
		// because it removes nothing.
		if err := c.store.Create(ctx, p); err != nil {
			appctx.GetLogger(ctx).Warn().Err(err).Str("userID", userid).Msg("failed to create empty share cache file")
		}
	}

	r := make(map[string]SpaceShareIDs, len(e.Doc.UserShares))
	for ssid, cached := range e.Doc.UserShares {
		r[ssid] = SpaceShareIDs{IDs: cached.IDs}
	}
	return r, nil
}

// Persist uploads the document of the user or group, also when nothing changed.
// It gives the document a new etag.
func (c *Cache) Persist(ctx context.Context, userid string) error {
	return c.store.Update(ctx, c.userCreatedPath(userid), func(*UserShareCache) (bool, error) {
		return true, nil
	})
}
