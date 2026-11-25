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

package providercache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/docstore"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	rtrace "github.com/owncloud/reva/v2/pkg/trace"
)

const tracerName = "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"

// startSpan starts a span with the tracer provider of the context. Prefer it
// to the global provider: in the single binary, each service sets the global
// provider at its start, so it belongs to the service that started last. A
// context with no provider uses the global provider.
func startSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return rtrace.ContextGetTracerProviderOrDefault(ctx).Tracer(tracerName).Start(ctx, name)
}

// Cache holds the share information of all spaces. It keeps one document for
// each space, in a docstore.
//
// The shares that the cache returns are read-only. A caller must not change
// them. A caller that needs to change a share calls proto.Clone first.
type Cache struct {
	store   *docstore.Store[Shares]
	storage metadata.Storage
}

// Shares holds the share information of one space
type Shares struct {
	Shares map[string]*collaboration.Share

	Etag string
}

// UnmarshalJSON overrides the default unmarshaling
// Shares are tricky to unmarshal because they contain an interface (Grantee) which makes the json Unmarshal bail out
// To work around that problem we unmarshal into json.RawMessage in a first step and then try to manually unmarshal
// into the specific types in a second step.
func (s *Shares) UnmarshalJSON(data []byte) error {
	tmp := struct {
		Shares map[string]json.RawMessage
	}{}

	err := json.Unmarshal(data, &tmp)
	if err != nil {
		return err
	}

	s.Shares = make(map[string]*collaboration.Share, len(tmp.Shares))
	for id, genericShare := range tmp.Shares {
		userShare := &collaboration.Share{
			Grantee: &provider.Grantee{Id: &provider.Grantee_UserId{}},
		}
		err = json.Unmarshal(genericShare, userShare) // is this a user share?
		if err == nil && userShare.Grantee.Type == provider.GranteeType_GRANTEE_TYPE_USER {
			s.Shares[id] = userShare
			continue
		}

		groupShare := &collaboration.Share{
			Grantee: &provider.Grantee{Id: &provider.Grantee_GroupId{}},
		}
		err = json.Unmarshal(genericShare, groupShare) // is this a group share?
		if err == nil && groupShare.Grantee.Type == provider.GranteeType_GRANTEE_TYPE_GROUP {
			s.Shares[id] = groupShare
			continue
		}

		invalidShare := &collaboration.Share{}
		err = json.Unmarshal(genericShare, invalidShare) // invalid
		if err == nil {
			s.Shares[id] = invalidShare
			continue
		}

		return err
	}

	return nil
}

var codec = docstore.Codec[Shares]{
	New: func() *Shares { return &Shares{Shares: map[string]*collaboration.Share{}} },
	Clone: func(s *Shares) *Shares {
		// A share is never changed in place. A change replaces it with a clone.
		c := &Shares{Shares: make(map[string]*collaboration.Share, len(s.Shares)), Etag: s.Etag}
		for id, sh := range s.Shares {
			c.Shares[id] = sh
		}
		return c
	},
	Decode: func(data []byte) (*Shares, error) {
		s := &Shares{}
		if err := json.Unmarshal(data, s); err != nil {
			return nil, err
		}
		return s, nil
	},
	Encode: func(s *Shares, etag string) ([]byte, error) {
		c := *s
		c.Etag = etag
		return json.Marshal(&c)
	},
	SetEtag: func(s *Shares, etag string) { s.Etag = etag },
}

// New returns a new Cache instance. The ttl is not used.
func New(s metadata.Storage, _ time.Duration) Cache {
	return Cache{
		store: docstore.New(s, codec, docstore.Options{
			Tracer: func(ctx context.Context) trace.Tracer {
				return rtrace.ContextGetTracerProviderOrDefault(ctx).Tracer(tracerName)
			},
			PathAttrs: func(p string) []attribute.KeyValue {
				return []attribute.KeyValue{attribute.String("cs3.spaceid", strings.TrimSuffix(filepath.Base(p), ".json"))}
			},
		}),
		storage: s,
	}
}

func spaceJSONPath(storageID, spaceID string) string {
	return filepath.Join("/storages", storageID, spaceID+".json")
}

func finish(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	span.SetStatus(codes.Ok, "")
}

// Add adds a share to the cache
func (c *Cache) Add(ctx context.Context, storageID, spaceID, shareID string, share *collaboration.Share) (err error) {
	ctx, span := startSpan(ctx, "Add")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID), attribute.String("cs3.shareid", shareID))

	switch {
	case storageID == "":
		return fmt.Errorf("missing storage id")
	case spaceID == "":
		return fmt.Errorf("missing space id")
	case shareID == "":
		return fmt.Errorf("missing share id")
	}

	// The cache keeps its own copy. The caller can change its share later.
	stored := proto.Clone(share).(*collaboration.Share)
	log := appctx.GetLogger(ctx).With().
		Str("hostname", os.Getenv("HOSTNAME")).
		Str("storageID", storageID).
		Str("spaceID", spaceID).
		Str("shareID", shareID).Logger()

	err = c.store.Update(ctx, spaceJSONPath(storageID, spaceID), func(d *Shares) (bool, error) {
		d.Shares[shareID] = stored
		return true, nil
	})
	if err != nil {
		log.Error().Err(err).Msg("persisting added provider share failed")
	}
	return err
}

// Remove removes a share from the cache
func (c *Cache) Remove(ctx context.Context, storageID, spaceID, shareID string) error {
	_, err := c.RemoveMany(ctx, storageID, spaceID, []string{shareID})
	return err
}

// RemoveMany removes the given shares of one space with one upload.
// It does not upload when none of the shares is in the space.
// It returns the IDs that this call removed. A share that another caller
// removed first is not in the result. The CAS upload makes sure that only one
// caller removes a share, also across pods.
func (c *Cache) RemoveMany(ctx context.Context, storageID, spaceID string, shareIDs []string) (_ []string, err error) {
	ctx, span := startSpan(ctx, "Remove")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID), attribute.Int("cs3.sharecount", len(shareIDs)))

	var removed []string
	err = c.store.Update(ctx, spaceJSONPath(storageID, spaceID), func(d *Shares) (bool, error) {
		// The store can call this function more than once. Each call starts
		// with an empty result.
		removed = nil
		for _, shareID := range shareIDs {
			if _, ok := d.Shares[shareID]; ok {
				delete(d.Shares, shareID)
				removed = append(removed, shareID)
			}
		}
		return len(removed) > 0, nil
	})
	if err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Str("storageID", storageID).Str("spaceID", spaceID).Msg("persisting removed provider share failed")
		return nil, err
	}
	return removed, nil
}

// Update changes one share and uploads the space. The update function gets a
// copy of the share. A CAS conflict reads the space again and calls the update
// function again with the new share. The cache keeps the old share until the
// upload succeeds.
// Update returns NotFound when the space does not have the share. Then it does
// not upload, so it does not write a share that a parallel caller removed.
func (c *Cache) Update(ctx context.Context, storageID, spaceID, shareID string, update func(*collaboration.Share)) (_ *collaboration.Share, err error) {
	ctx, span := startSpan(ctx, "Update")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID), attribute.String("cs3.shareid", shareID))

	switch {
	case storageID == "":
		return nil, fmt.Errorf("missing storage id")
	case spaceID == "":
		return nil, fmt.Errorf("missing space id")
	case shareID == "":
		return nil, fmt.Errorf("missing share id")
	}

	var updated *collaboration.Share
	err = c.store.Update(ctx, spaceJSONPath(storageID, spaceID), func(d *Shares) (bool, error) {
		updated = nil
		old := d.Shares[shareID]
		if old == nil {
			return false, nil // the share is gone, do not upload
		}
		s := proto.Clone(old).(*collaboration.Share)
		update(s)
		d.Shares[shareID] = s
		updated = s
		return true, nil
	})
	if err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Str("storageID", storageID).Str("spaceID", spaceID).Str("shareID", shareID).Msg("persisting updated provider share failed")
		return nil, err
	}
	if updated == nil {
		return nil, errtypes.NotFound(shareID)
	}
	return updated, nil
}

// Get returns one share. With skipSync, it reads the share from memory and
// makes no download. The share is read-only.
func (c *Cache) Get(ctx context.Context, storageID, spaceID, shareID string, skipSync bool) (_ *collaboration.Share, err error) {
	ctx, span := startSpan(ctx, "Get")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID), attribute.String("cs3.shareid", shareID))

	p := spaceJSONPath(storageID, spaceID)
	if skipSync {
		e := c.store.Peek(p)
		if e == nil {
			return nil, nil
		}
		return e.Doc.Shares[shareID], nil
	}
	e, err := c.store.Get(ctx, p)
	if err != nil {
		return nil, err
	}
	return e.Doc.Shares[shareID], nil
}

// All reads all spaces from the storage. It returns the shares of each space,
// sorted by storage ID and space ID. The shares are read-only.
func (c *Cache) All(ctx context.Context) (_ map[string]map[string]*Shares, err error) {
	ctx, span := startSpan(ctx, "All")
	defer span.End()
	defer func() { finish(span, err) }()

	providers, err := c.storage.ListDir(ctx, "/storages")
	if err != nil {
		return nil, err
	}
	all := map[string]map[string]*Shares{}
	for _, provider := range providers {
		storageID := provider.Name
		spaces, err := c.storage.ListDir(ctx, path.Join("/storages", storageID))
		if err != nil {
			return nil, err
		}
		all[storageID] = map[string]*Shares{}
		for _, space := range spaces {
			spaceID := strings.TrimSuffix(space.Name, ".json")
			e, err := c.store.Get(ctx, spaceJSONPath(storageID, spaceID))
			if err != nil {
				return nil, err
			}
			all[storageID][spaceID] = e.Doc
		}
	}
	return all, nil
}

// ListSpace returns the list of shares in a given space. The shares are
// read-only. The etag is empty when the space has no file.
func (c *Cache) ListSpace(ctx context.Context, storageID, spaceID string) (_ *Shares, err error) {
	ctx, span := startSpan(ctx, "ListSpace")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID))

	e, err := c.store.Get(ctx, spaceJSONPath(storageID, spaceID))
	if err != nil {
		return nil, err
	}
	return &Shares{Shares: e.Doc.Shares, Etag: e.Etag}, nil
}

// Persist uploads the space document, also when nothing changed. It gives the
// document a new etag.
func (c *Cache) Persist(ctx context.Context, storageID, spaceID string) (err error) {
	ctx, span := startSpan(ctx, "Persist")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.storageid", storageID), attribute.String("cs3.spaceid", spaceID))

	return c.store.Update(ctx, spaceJSONPath(storageID, spaceID), func(*Shares) (bool, error) { return true, nil })
}

// PurgeSpace removes all shares of a space. It writes an empty document.
func (c *Cache) PurgeSpace(ctx context.Context, storageID, spaceID string) (err error) {
	ctx, span := startSpan(ctx, "PurgeSpace")
	defer span.End()
	defer func() { finish(span, err) }()

	return c.store.Update(ctx, spaceJSONPath(storageID, spaceID), func(d *Shares) (bool, error) {
		d.Shares = map[string]*collaboration.Share{}
		return true, nil
	})
}
