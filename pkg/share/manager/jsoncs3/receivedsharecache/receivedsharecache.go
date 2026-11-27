// Copyright 2018-2022 CERN
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

package receivedsharecache

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/docstore"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// name is the Tracer name used to identify this instrumentation library.
const tracerName = "receivedsharecache"

// Cache stores the list of received shares and their states. It keeps one
// document for each user, in a docstore. The storage is sharded by user.
//
// The documents that Get and List return are read-only. A caller must not
// change them.
type Cache struct {
	store *docstore.Store[Spaces]
}

// Spaces holds the received shares of one user per space
type Spaces struct {
	Spaces map[string]*Space

	etag string
}

// Space holds the received shares of one user in one space
type Space struct {
	States map[string]*State
}

// State holds the state information of a received share
type State struct {
	State      collaboration.ShareState
	MountPoint *provider.Reference
	Hidden     bool
}

var codec = docstore.Codec[Spaces]{
	New: func() *Spaces { return &Spaces{Spaces: map[string]*Space{}} },
	Clone: func(s *Spaces) *Spaces {
		c := &Spaces{Spaces: make(map[string]*Space, len(s.Spaces)), etag: s.etag}
		for id, space := range s.Spaces {
			cp := &Space{States: make(map[string]*State, len(space.States))}
			for shareID, state := range space.States {
				// A state is never changed in place. A change replaces it.
				cp.States[shareID] = state
			}
			c.Spaces[id] = cp
		}
		return c
	},
	Decode: func(data []byte) (*Spaces, error) {
		s := &Spaces{}
		if err := json.Unmarshal(data, s); err != nil {
			return nil, err
		}
		if s.Spaces == nil {
			s.Spaces = map[string]*Space{}
		}
		for id, space := range s.Spaces {
			if space == nil {
				s.Spaces[id] = &Space{States: map[string]*State{}}
			} else if space.States == nil {
				space.States = map[string]*State{}
			}
		}
		return s, nil
	},
	Encode:  func(s *Spaces, _ string) ([]byte, error) { return json.Marshal(s) },
	SetEtag: func(s *Spaces, etag string) { s.etag = etag },
}

// New returns a new Cache instance. The ttl is not used.
func New(s metadata.Storage, _ time.Duration) Cache {
	return Cache{
		store: docstore.New(s, codec, docstore.Options{
			Tracer: func(ctx context.Context) trace.Tracer { return appctx.GetTracerProvider(ctx).Tracer(tracerName) },
			PathAttrs: func(p string) []attribute.KeyValue {
				return []attribute.KeyValue{attribute.String("cs3.userid", filepath.Base(filepath.Dir(p)))}
			},
		}),
	}
}

func userJSONPath(userID string) string {
	return filepath.Join("/users", userID, "received.json")
}

func sameState(a, b *State) bool {
	return a != nil && b != nil && a.State == b.State && a.Hidden == b.Hidden && proto.Equal(a.MountPoint, b.MountPoint)
}

func finish(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	span.SetStatus(codes.Ok, "")
}

// Add adds a new entry to the cache
func (c *Cache) Add(ctx context.Context, userID, spaceID string, rs *collaboration.ReceivedShare) (err error) {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "Add")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.userid", userID), attribute.String("cs3.spaceid", spaceID))

	shareID := rs.Share.Id.GetOpaqueId()
	state := &State{State: rs.State, MountPoint: rs.MountPoint, Hidden: rs.Hidden}
	return c.store.Update(ctx, userJSONPath(userID), func(d *Spaces) (bool, error) {
		space := d.Spaces[spaceID]
		if space == nil {
			space = &Space{States: map[string]*State{}}
			d.Spaces[spaceID] = space
		}
		if sameState(space.States[shareID], state) {
			return false, nil
		}
		space.States[shareID] = state
		return true, nil
	})
}

// Get returns one entry from the cache. The state is read-only.
func (c *Cache) Get(ctx context.Context, userID, spaceID, shareID string) (_ *State, err error) {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "Get")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.userid", userID))

	e, err := c.store.Get(ctx, userJSONPath(userID))
	if err != nil {
		return nil, err
	}
	space := e.Doc.Spaces[spaceID]
	if space == nil {
		return nil, nil
	}
	return space.States[shareID], nil
}

// Remove removes an entry from the cache
func (c *Cache) Remove(ctx context.Context, userID, spaceID, shareID string) error {
	return c.RemoveMany(ctx, userID, spaceID, []string{shareID})
}

// RemoveMany removes the given shares of one user in one space with one upload.
// It does not upload when none of the shares is in the document.
func (c *Cache) RemoveMany(ctx context.Context, userID, spaceID string, shareIDs []string) (err error) {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "Remove")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.userid", userID), attribute.String("cs3.spaceid", spaceID), attribute.Int("cs3.sharecount", len(shareIDs)))

	return c.store.Update(ctx, userJSONPath(userID), func(d *Spaces) (bool, error) {
		space := d.Spaces[spaceID]
		if space == nil {
			return false, nil
		}
		removed := false
		for _, shareID := range shareIDs {
			if _, ok := space.States[shareID]; ok {
				delete(space.States, shareID)
				removed = true
			}
		}
		if !removed {
			return false, nil
		}
		if len(space.States) == 0 {
			delete(d.Spaces, spaceID)
		}
		return true, nil
	})
}

// List returns a list of received shares for a given user. The result is
// read-only: a caller must not change it.
func (c *Cache) List(ctx context.Context, userID string) (_ map[string]*Space, err error) {
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "List")
	defer span.End()
	defer func() { finish(span, err) }()
	span.SetAttributes(attribute.String("cs3.userid", userID))

	e, err := c.store.Get(ctx, userJSONPath(userID))
	if err != nil {
		return nil, err
	}
	return e.Doc.Spaces, nil
}
