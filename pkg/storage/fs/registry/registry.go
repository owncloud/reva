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

package registry

import (
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/rs/zerolog"
)

// NewFunc is the function that storage implementations
// should register at init time.
type NewFunc func(map[string]interface{}, events.Stream, *zerolog.Logger) (storage.FS, error)

// NewFuncs is a map containing all the registered storage backends.
var NewFuncs = map[string]NewFunc{}

// Capabilities holds the capability set each registered driver declares about
// itself, keyed by the driver name passed to Register. Read at init time by
// the OCS /cloud/capabilities handler. Absent keys default to false on the
// wire. See OCISDEV-904.
//
// Caveat: keyed by driver name. In a deployment that overrides `mount_id`
// (so StorageSpace.Root.StorageId != driver name), the OCS response keys
// won't match the wire StorageId. Fix is to register from New() once
// mount_id is known; deferred — v1 PoC uses default mount IDs.
var Capabilities = map[string]storage.Capabilities{}

// Register registers a new storage backend with its capability declaration.
// Not safe for concurrent use. Safe for use from package init.
func Register(name string, f NewFunc, caps storage.Capabilities) {
	NewFuncs[name] = f
	Capabilities[name] = caps
}

// FullCapabilities is the historical decomposedfs-shaped set used by most
// in-tree drivers. Note: editing this affects every driver that registered
// with it — when adding a new key, audit each caller; don't assume the set
// is "free."
var FullCapabilities = storage.Capabilities{
	Upload: true, Sharing: true, Locks: true, Versions: true,
	Trash: true, Tags: true, Favorites: true, Search: true,
}
