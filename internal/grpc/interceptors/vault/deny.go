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

package vault

import (
	"context"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	rstatus "github.com/owncloud/reva/v2/pkg/rgrpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// denyResponse returns a PERMISSION_DENIED response of the type that matches the request.
// For an unknown request type it returns a PERMISSION_DENIED gRPC error.
func denyResponse(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, msg string) (interface{}, error) {
	switch req.(type) {
	case *provider.StatRequest:
		return &provider.StatResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.ListContainerRequest:
		return &provider.ListContainerResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.GetPathRequest:
		return &provider.GetPathResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.GetQuotaRequest:
		return &provider.GetQuotaResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.InitiateFileDownloadRequest:
		return &provider.InitiateFileDownloadResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.InitiateFileUploadRequest:
		return &provider.InitiateFileUploadResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.CreateContainerRequest:
		return &provider.CreateContainerResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.TouchFileRequest:
		return &provider.TouchFileResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.DeleteRequest:
		return &provider.DeleteResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.MoveRequest:
		return &provider.MoveResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.CreateHomeRequest:
		return &provider.CreateHomeResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.AddGrantRequest:
		return &provider.AddGrantResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.RemoveGrantRequest:
		return &provider.RemoveGrantResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.UpdateGrantRequest:
		return &provider.UpdateGrantResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.ListGrantsRequest:
		return &provider.ListGrantsResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.ListFileVersionsRequest:
		return &provider.ListFileVersionsResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.RestoreFileVersionRequest:
		return &provider.RestoreFileVersionResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.ListRecycleRequest:
		return &provider.ListRecycleResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.RestoreRecycleItemRequest:
		return &provider.RestoreRecycleItemResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.PurgeRecycleRequest:
		return &provider.PurgeRecycleResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.SetArbitraryMetadataRequest:
		return &provider.SetArbitraryMetadataResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.UnsetArbitraryMetadataRequest:
		return &provider.UnsetArbitraryMetadataResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.ListStorageSpacesRequest:
		return &provider.ListStorageSpacesResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.CreateStorageSpaceRequest:
		return &provider.CreateStorageSpaceResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.UpdateStorageSpaceRequest:
		return &provider.UpdateStorageSpaceResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	case *provider.DeleteStorageSpaceRequest:
		return &provider.DeleteStorageSpaceResponse{Status: rstatus.NewPermissionDenied(ctx, nil, msg)}, nil
	default:
		appctx.GetLogger(ctx).Debug().Str("method", info.FullMethod).Msg("vault: blocking unknown request type")
		return nil, grpcstatus.Errorf(codes.PermissionDenied, "vault: %s: %T", msg, req)
	}
}
