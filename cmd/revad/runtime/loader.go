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

package runtime

import (
	// These are all the extensions points for REVA
	_ "github.com/owncloud/reva/v2/internal/grpc/interceptors/loader"                 // used by all reva services
	_ "github.com/owncloud/reva/v2/internal/grpc/services/loader"                     // used by all reva services
	_ "github.com/owncloud/reva/v2/internal/http/interceptors/auth/credential/loader" // used by ocm (?)
	_ "github.com/owncloud/reva/v2/internal/http/interceptors/loader"                 // used by all reva services
	_ "github.com/owncloud/reva/v2/internal/http/services/loader"                     // used by multiple reva services
	_ "github.com/owncloud/reva/v2/pkg/app/provider/loader"                           // used by app-provider
	_ "github.com/owncloud/reva/v2/pkg/app/registry/loader"                           // used by app-registry
	_ "github.com/owncloud/reva/v2/pkg/appauth/manager/loader"                        // used by auth-app
	_ "github.com/owncloud/reva/v2/pkg/auth/manager/loader"                           // used by all reva services
	_ "github.com/owncloud/reva/v2/pkg/auth/registry/loader"                          // used by gateway, storage-system
	_ "github.com/owncloud/reva/v2/pkg/group/manager/loader"                          // used by groups
	_ "github.com/owncloud/reva/v2/pkg/ocm/invite/repository/loader"                  // used by ocm
	_ "github.com/owncloud/reva/v2/pkg/ocm/provider/authorizer/loader"                // used by ocm
	_ "github.com/owncloud/reva/v2/pkg/ocm/share/repository/loader"                   // used by ocm
	_ "github.com/owncloud/reva/v2/pkg/permission/manager/loader"                     // used by storage-system
	_ "github.com/owncloud/reva/v2/pkg/publicshare/manager/loader"                    // used by sharing (?)
	_ "github.com/owncloud/reva/v2/pkg/rhttp/datatx/manager/loader"                   // used by storage-users, storage-system, frontend, gateway, ocm
	_ "github.com/owncloud/reva/v2/pkg/share/cache/warmup/loader"                     // used by frontend
	_ "github.com/owncloud/reva/v2/pkg/share/manager/loader"                          // used by sharing
	_ "github.com/owncloud/reva/v2/pkg/storage/fs/loader"                             // used by storage-users, storage-system and others
	_ "github.com/owncloud/reva/v2/pkg/storage/registry/loader"                       // used by gateway, storage-system
	_ "github.com/owncloud/reva/v2/pkg/user/manager/loader"                           // used by users
	// Not needed?
	// _ "github.com/owncloud/reva/v2/internal/http/interceptors/auth/token/loader"
	// _ "github.com/owncloud/reva/v2/internal/http/interceptors/auth/tokenwriter/loader"
	// _ "github.com/owncloud/reva/v2/pkg/cbox/loader" (?)
	// _ "github.com/owncloud/reva/v2/pkg/datatx/manager/loader" (?)
	// _ "github.com/owncloud/reva/v2/pkg/metrics/driver/loader"
	// _ "github.com/owncloud/reva/v2/pkg/preferences/loader"
	// _ "github.com/owncloud/reva/v2/pkg/storage/favorite/loader"
	// _ "github.com/owncloud/reva/v2/pkg/token/manager/loader"
)
