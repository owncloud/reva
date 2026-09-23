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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocs/config"
	"github.com/owncloud/reva/v2/pkg/owncloud/ocs"
	"github.com/owncloud/reva/v2/pkg/utils"
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

// getCapabilities performs a GetCapabilities request against the handler and returns the
// capabilities carried by the OCS response.
func getCapabilities(t *testing.T, h *Handler, target string) *ocs.Capabilities {
	t.Helper()

	w := httptest.NewRecorder()
	h.GetCapabilities(w, httptest.NewRequest(http.MethodGet, target, nil))

	var payload struct {
		OCS struct {
			Data ocs.CapabilitiesData `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("cant unmarshal response for %s: %v", target, err)
	}
	if payload.OCS.Data.Capabilities == nil {
		t.Fatalf("no capabilities in response for %s", target)
	}
	return payload.OCS.Data.Capabilities
}

func newVaultHandler(t *testing.T, vaultEnabled bool, filesSharing *ocs.CapabilitiesFilesSharing) *Handler {
	t.Helper()

	vault := &ocs.CapabilitiesVault{}
	if vaultEnabled {
		vault.Enabled = true
	}

	h := &Handler{}
	h.Init(&config.Config{
		Capabilities: ocs.CapabilitiesData{
			Capabilities: &ocs.Capabilities{
				Vault:        vault,
				FilesSharing: filesSharing,
			},
		},
	})
	return h
}

// The vault storage provider id must be announced on every capabilities response, not only
// on `?vault=true`. Clients booted outside the vault fetch capabilities without that query
// parameter and need the id to recognize vault resources.
func TestGetCapabilitiesAnnouncesVaultStorageProvider(t *testing.T) {
	h := newVaultHandler(t, true, &ocs.CapabilitiesFilesSharing{
		APIEnabled: true,
		Public:     &ocs.CapabilitiesFilesSharingPublic{Enabled: true},
	})

	for _, target := range []string{"/capabilities?format=json", "/capabilities?format=json&vault=true"} {
		caps := getCapabilities(t, h, target)
		if caps.Vault == nil {
			t.Fatalf("no vault capabilities in response for %s", target)
		}
		if caps.Vault.VaultStorageProvider != utils.VaultStorageProviderID {
			t.Errorf("%s: got vault storage provider %q, want %q",
				target, caps.Vault.VaultStorageProvider, utils.VaultStorageProviderID)
		}
	}

	// the vault scope still turns public sharing off, and only for that scope
	if caps := getCapabilities(t, h, "/capabilities?format=json&vault=true"); bool(caps.FilesSharing.Public.Enabled) {
		t.Error("vault capabilities should have public sharing disabled")
	}
	if caps := getCapabilities(t, h, "/capabilities?format=json"); !bool(caps.FilesSharing.Public.Enabled) {
		t.Error("non-vault capabilities should keep public sharing enabled")
	}
}

func TestGetCapabilitiesOmitsVaultStorageProviderWhenVaultDisabled(t *testing.T) {
	h := newVaultHandler(t, false, &ocs.CapabilitiesFilesSharing{APIEnabled: true})

	caps := getCapabilities(t, h, "/capabilities?format=json")
	if caps.Vault == nil {
		t.Fatal("no vault capabilities in response")
	}
	if caps.Vault.VaultStorageProvider != "" {
		t.Errorf("got vault storage provider %q, want it empty while vault mode is disabled",
			caps.Vault.VaultStorageProvider)
	}
}
