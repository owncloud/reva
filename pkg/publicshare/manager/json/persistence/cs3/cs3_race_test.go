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

package cs3_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json/persistence"
	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json/persistence/cs3"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/stretchr/testify/require"
)

// TestConcurrentReadWrite hammers Read/Write from many goroutines at once.
// Read's lazy cache refill (see cs3.go) mutates internal state whenever it
// sees a newer remote mtime, so overlapping Read calls right after a Write
// are exactly the window that needs cs3's own mutex, independent of whatever
// lock a caller happens to hold. Under `go test -race`, removing that mutex
// makes this test fail; this pins that regression down.
func TestConcurrentReadWrite(t *testing.T) {
	tmpdir, err := os.MkdirTemp("", "cs3-race-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	storage, err := metadata.NewDiskStorage(tmpdir)
	require.NoError(t, err)

	p := cs3.New(storage)
	require.NoError(t, p.Init(context.Background()))

	const writers = 8
	const readers = 8
	const iterations = 500

	var wg sync.WaitGroup

	wg.Add(writers)
	for w := range writers {
		go func() {
			defer wg.Done()
			for i := range iterations {
				db := persistence.PublicShares{
					fmt.Sprintf("w%d-%d", w, i): map[string]interface{}{"share": "{}", "password": ""},
				}
				_ = p.Write(context.Background(), db)
			}
		}()
	}

	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			for range iterations {
				_, _ = p.Read(context.Background())
			}
		}()
	}

	wg.Wait()
}
