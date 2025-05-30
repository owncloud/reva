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

package net

import (
	"github.com/owncloud/reva/v2/internal/http/services/datagateway"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
)

type ctxKey int

const (
	// AccessTokenIndex specifies the index of the Reva access token in a context.
	AccessTokenIndex ctxKey = iota
	// AccessTokenName specifies the name of the Reva access token used during requests.
	AccessTokenName = ctxpkg.TokenHeader
	// TransportTokenName specifies the name of the Reva transport token used during data transfers.
	TransportTokenName = datagateway.TokenTransportHeader
)
