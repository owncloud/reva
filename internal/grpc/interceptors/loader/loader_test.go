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

package loader

import (
	"testing"

	"github.com/owncloud/reva/v2/pkg/rgrpc"
)

// TestVaultInterceptorIsLoaded protects the vault storage provider. rgrpc ignores a configured
// interceptor that is not registered, so a missing import would leave the vault unprotected.
func TestVaultInterceptorIsLoaded(t *testing.T) {
	if _, ok := rgrpc.UnaryInterceptors["vault"]; !ok {
		t.Error("the unary vault interceptor is not registered")
	}
	if _, ok := rgrpc.StreamInterceptors["vault"]; !ok {
		t.Error("the stream vault interceptor is not registered")
	}
}
