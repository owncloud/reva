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

package ocm

import (
	"encoding/xml"
	"testing"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/studio-b12/gowebdav"
)

// propsWithLockdiscovery builds a gowebdav.Props carrying the given raw
// lockdiscovery value, mirroring what UnmarshalXML produces from a PROPFIND
// response. When raw is nil the key is absent, which is the unlocked case.
func propsWithLockdiscovery(t *testing.T, raw *string) gowebdav.Props {
	t.Helper()

	body := `<?xml version="1.0"?>
<d:prop xmlns:d="DAV:">
  <d:getetag>"abc"</d:getetag>
`
	if raw != nil {
		body += "  <d:lockdiscovery>" + *raw + "</d:lockdiscovery>\n"
	}
	body += "</d:prop>"

	var props gowebdav.Props
	if err := xml.Unmarshal([]byte(body), &props); err != nil {
		t.Fatalf("could not build props: %v", err)
	}
	return props
}

func strptr(s string) *string { return &s }

func TestExtractLock(t *testing.T) {
	// as emitted by ocdav's activeLocks for an exclusive lock
	exclusive := `<d:activelock>` +
		`<d:locktype><d:write/></d:locktype>` +
		`<d:lockscope><d:exclusive/></d:lockscope>` +
		`<d:depth>Infinity</d:depth>` +
		`<d:owner>some-user@https://localhost:9200</d:owner>` +
		`<d:timeout>Second-1800</d:timeout>` +
		`<d:locktoken><d:href>opaque-lock-token</d:href></d:locktoken>` +
		`</d:activelock>`

	shared := `<d:activelock>` +
		`<d:lockscope><d:shared/></d:lockscope>` +
		`<d:timeout>Infinity</d:timeout>` +
		`<d:locktoken><d:href>shared-token</d:href></d:locktoken>` +
		`</d:activelock>`

	// activeLocks omits d:locktoken entirely when the lock carries no id
	noToken := `<d:activelock>` +
		`<d:lockscope><d:exclusive/></d:lockscope>` +
		`<d:timeout>Infinity</d:timeout>` +
		`</d:activelock>`

	tests := []struct {
		name     string
		raw      *string
		wantID   string
		wantType provider.LockType
	}{
		{
			name:     "exclusive lock yields its token",
			raw:      strptr(exclusive),
			wantID:   "opaque-lock-token",
			wantType: provider.LockType_LOCK_TYPE_EXCL,
		},
		{
			name:     "shared lock keeps its scope",
			raw:      strptr(shared),
			wantID:   "shared-token",
			wantType: provider.LockType_LOCK_TYPE_SHARED,
		},
		{
			// the regression that made every unlocked OCM file read as locked
			name:   "absent lockdiscovery is not a lock",
			raw:    nil,
			wantID: "",
		},
		{
			name:   "lock without a token is not usable",
			raw:    strptr(noToken),
			wantID: "",
		},
		{
			name:   "empty lockdiscovery is not a lock",
			raw:    strptr(""),
			wantID: "",
		},
		{
			name:   "unrelated content is not a lock",
			raw:    strptr("garbage"),
			wantID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLock(propsWithLockdiscovery(t, tt.raw))

			if tt.wantID == "" {
				if got != nil {
					t.Fatalf("expected no lock, got %+v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected a lock, got nil")
			}
			if got.LockId != tt.wantID {
				t.Errorf("LockId = %q, want %q", got.LockId, tt.wantID)
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", got.Type, tt.wantType)
			}
		})
	}
}

// The bug this replaced: gowebdav renders a missing prop as the string "<nil>",
// so a guard on emptiness never fires and callers testing the returned pointer
// against nil read an unlocked file as locked.
func TestExtractLockAbsentPropIsNotEmptyString(t *testing.T) {
	props := propsWithLockdiscovery(t, nil)

	if raw := props.GetString(xml.Name{Space: "DAV:", Local: "lockdiscovery"}); raw != "<nil>" {
		t.Skipf("gowebdav no longer formats absent props as <nil> (got %q); the guard in extractLock can be simplified", raw)
	}
	if got := extractLock(props); got != nil {
		t.Fatalf("expected no lock for absent lockdiscovery, got %+v", got)
	}
}
