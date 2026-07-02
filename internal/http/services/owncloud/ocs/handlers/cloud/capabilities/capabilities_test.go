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

package capabilities

import (
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"testing"

	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocs/config"
	"github.com/owncloud/reva/v2/pkg/owncloud/ocs"
	"github.com/owncloud/reva/v2/pkg/storage"
	fsregistry "github.com/owncloud/reva/v2/pkg/storage/fs/registry"
)

func TestMarshal(t *testing.T) {
	cd := ocs.CapabilitiesData{
		Capabilities: &ocs.Capabilities{
			FilesSharing: &ocs.CapabilitiesFilesSharing{
				APIEnabled: true,
			},
		},
	}

	// TODO: remove resharing from these strings once web defaults to resharing=false
	jsonExpect := `{"capabilities":{"core":null,"checksums":null,"files":null,"dav":null,"files_sharing":{"api_enabled":true,"group_sharing":false,"sharing_roles":false,"deny_access":false,"auto_accept_share":false,"share_with_group_members_only":false,"share_with_membership_groups_only":false,"search_min_length":0,"default_permissions":0,"user_enumeration":null,"federation":null,"public":null,"user":null,"resharing":false}},"version":null}`
	xmlExpect := `<CapabilitiesData><capabilities><files_sharing><api_enabled>1</api_enabled><group_sharing>0</group_sharing><sharing_roles>0</sharing_roles><deny_access>0</deny_access><auto_accept_share>0</auto_accept_share><share_with_group_members_only>0</share_with_group_members_only><share_with_membership_groups_only>0</share_with_membership_groups_only><search_min_length>0</search_min_length><default_permissions>0</default_permissions><resharing>0</resharing></files_sharing></capabilities></CapabilitiesData>`

	jsonData, err := json.Marshal(&cd)
	if err != nil {
		t.Fatal("cant marshal json")
	}

	if string(jsonData) != jsonExpect {
		t.Log(string(jsonData))
		t.Fatal("json data does not match")
	}

	xmlData, err := xml.Marshal(&cd)
	if err != nil {
		t.Fatal("cant marshal xml")
	}

	if string(xmlData) != xmlExpect {
		t.Log(string(xmlData))
		t.Fatal("xml data does not match")
	}
}

// TestGetCapabilitiesExposesPerProviderSection asserts that the
// /cloud/capabilities response carries a per-provider capability section keyed
// by driver name, sourced from the storage-fs registry that drivers populate
// at init time. Absent keys default to false. See OCISDEV-904.
func TestGetCapabilitiesExposesPerProviderSection(t *testing.T) {
	// Replace the in-process capability registry for the duration of this
	// test, then restore.
	defer func(prev map[string]storage.Capabilities) { fsregistry.Capabilities = prev }(fsregistry.Capabilities)
	fsregistry.Capabilities = map[string]storage.Capabilities{
		"decomposedfs": fsregistry.FullCapabilities,
		"kiteworks":    {},
	}

	h := &Handler{}
	h.Init(&config.Config{})

	req := httptest.NewRequest("GET", "/ocs/v1.php/cloud/capabilities?format=json", nil)
	w := httptest.NewRecorder()
	h.GetCapabilities(w, req)

	var envelope struct {
		OCS struct {
			Data ocs.CapabilitiesData `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	caps := envelope.OCS.Data.Capabilities
	if caps == nil {
		t.Fatal("response has no capabilities")
	}
	if caps.Providers == nil {
		t.Fatal("response has no providers section")
	}

	kw, ok := caps.Providers["kiteworks"]
	if !ok {
		t.Fatal("response missing kiteworks provider")
	}
	if kw.Upload {
		t.Error("kiteworks must not declare Upload")
	}
	if kw.Sharing || kw.Locks || kw.Versions || kw.Trash {
		t.Error("kiteworks must not declare any write-shaped capability")
	}

	dfs, ok := caps.Providers["decomposedfs"]
	if !ok {
		t.Fatal("response missing decomposedfs provider")
	}
	if !dfs.Upload {
		t.Error("decomposedfs must declare Upload")
	}
	if !dfs.Versions || !dfs.Trash {
		t.Error("decomposedfs must declare Versions and Trash")
	}
}
