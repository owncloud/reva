// Copyright 2018-2023 CERN
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

package metadata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	microstore "go-micro.dev/v4/store"

	"github.com/shamaton/msgpack/v2"
)

// failingPushCache is a FileMetadataCache that always reports a cache miss on
// read and always fails on write, simulating a dead NATS connection backing
// the metadata cache.
type failingPushCache struct{}

func (failingPushCache) PullFromCache(_ string, _ any) error {
	return errors.New("not found")
}

func (failingPushCache) PushToCache(_ string, _ any) error {
	return errors.New("nats: connection closed")
}

func (failingPushCache) List(_ ...microstore.ListOption) ([]string, error)   { return nil, nil }
func (failingPushCache) Delete(_ string, _ ...microstore.DeleteOption) error { return nil }
func (failingPushCache) Close() error                                        { return nil }
func (failingPushCache) RemoveMetadata(_ string) error                       { return nil }

// TestLoadAttributesToleratesCacheWriteFailure verifies that a successful read
// from disk is returned even when writing the attributes back into the cache
// fails (e.g. because the NATS connection is dead). Regression test for
// OCISDEV-834.
func TestLoadAttributesToleratesCacheWriteFailure(t *testing.T) {
	tmpdir, err := os.MkdirTemp(os.TempDir(), "MessagePackBackendInternalTest-")
	if err != nil {
		t.Fatalf("failed to create tmpdir: %v", err)
	}
	defer os.RemoveAll(tmpdir)

	b := MessagePackBackend{
		rootPath:  filepath.Clean(tmpdir),
		metaCache: failingPushCache{},
	}

	file := filepath.Join(tmpdir, "file")
	want := map[string][]byte{"foo": []byte("bar")}

	// Write the metadata file directly to disk, bypassing the cache, so that
	// loadAttributes has to fall through to the disk read.
	d, err := msgpack.Marshal(want)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(b.MetadataPath(file), d, 0600); err != nil {
		t.Fatalf("failed to write metadata file: %v", err)
	}

	// All() -> loadAttributes(): cache read misses, disk read succeeds, and the
	// cache write-back fails. The failing PushToCache must not turn this into
	// an error.
	got, err := b.All(context.Background(), file)
	if err != nil {
		t.Fatalf("All failed despite valid on-disk metadata: %v", err)
	}
	if string(got["foo"]) != "bar" {
		t.Fatalf("expected attribute value %q, got %q", "bar", string(got["foo"]))
	}
}
