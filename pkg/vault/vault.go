// Copyright 2018-2026 CERN
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

// Package vault provides the checks for resources in the vault storage provider.
package vault

import (
	"context"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"github.com/owncloud/reva/v2/pkg/permission"
	"github.com/owncloud/reva/v2/pkg/storagespace"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

// PermissionClient is the part of the gateway API that the vault checks use.
type PermissionClient interface {
	CheckPermission(ctx context.Context, in *permissions.CheckPermissionRequest, opts ...grpc.CallOption) (*permissions.CheckPermissionResponse, error)
}

// IsVaultStorage reports whether the storage ID belongs to the vault storage provider.
func IsVaultStorage(storageID string) bool {
	return storageID == utils.VaultStorageProviderID
}

// IsVaultSpace reports whether the space ID belongs to the vault storage provider.
// It returns true for a space ID that it cannot parse, so that callers fail closed.
func IsVaultSpace(spaceID string) bool {
	rid, err := storagespace.ParseID(spaceID)
	if err != nil {
		return true
	}
	return IsVaultStorage(rid.GetStorageId())
}

// NewCheckPermissionRequest returns the request that checks the VaultMode permission of the user.
func NewCheckPermissionRequest(userID *userpb.UserId) *permissions.CheckPermissionRequest {
	return &permissions.CheckPermissionRequest{
		SubjectRef: &permissions.SubjectReference{
			Spec: &permissions.SubjectReference_UserId{UserId: userID},
		},
		Permission: permission.VaultMode,
	}
}

// PermissionGranted interprets the status of a VaultMode check.
// Only OK and PERMISSION_DENIED are definitive answers. Each other status returns an error,
// so that callers fail closed.
func PermissionGranted(st *rpc.Status) (bool, error) {
	switch code := st.GetCode(); code {
	case rpc.Code_CODE_OK:
		return true, nil
	case rpc.Code_CODE_PERMISSION_DENIED:
		return false, nil
	default:
		return false, errors.Errorf("vault permission check returned status %q: %s", code, st.GetMessage())
	}
}

// HasVaultMode reports whether the user holds the VaultMode permission.
// A non-nil error means undetermined, so callers must treat the user as not allowed.
func HasVaultMode(ctx context.Context, client PermissionClient, userID *userpb.UserId) (bool, error) {
	res, err := client.CheckPermission(ctx, NewCheckPermissionRequest(userID))
	if err != nil {
		return false, errors.Wrap(err, "vault: error calling CheckPermission")
	}
	return PermissionGranted(res.GetStatus())
}
