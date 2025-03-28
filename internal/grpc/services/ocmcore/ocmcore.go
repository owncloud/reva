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

package ocmcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	ocmcore "github.com/cs3org/go-cs3apis/cs3/ocm/core/v1beta1"
	ocm "github.com/cs3org/go-cs3apis/cs3/sharing/ocm/v1beta1"
	providerpb "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/ocm/share"
	"github.com/owncloud/reva/v2/pkg/ocm/share/repository/registry"
	ocmuser "github.com/owncloud/reva/v2/pkg/ocm/user"
	"github.com/owncloud/reva/v2/pkg/rgrpc"
	"github.com/owncloud/reva/v2/pkg/rgrpc/status"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/sharedconf"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/owncloud/reva/v2/pkg/utils/cfg"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func init() {
	rgrpc.Register("ocmcore", New)
}

type config struct {
	Driver               string                            `mapstructure:"driver"`
	Drivers              map[string]map[string]interface{} `mapstructure:"drivers"`
	UserProviderEndpoint string                            `mapstructure:"user_provider_endpoint" env:"OCIS_USER_PROVIDER_ENDPOINT" desc:"The endpoint of the user provider service."`
	GatewayEndpoint      string                            `mapstructure:"gateway_endpoint" env:"OCIS_GATEWAY_ENDPOINT" desc:"The endpoint of the gateway service."`
	ServiceAccountID     string                            `mapstructure:"service_account_id" env:"OCIS_SERVICE_ACCOUNT_ID" desc:"The ID of the service account to use for authentication."`
	ServiceAccountSecret string                            `mapstructure:"service_account_secret" env:"OCIS_SERVICE_ACCOUNT_SECRET" desc:"The secret of the service account to use for authentication."`
	GRPCClientOptions    map[string]interface{}            `mapstructure:"grpc_client_options"`
}

type service struct {
	conf            *config
	repo            share.Repository
	gatewaySelector pool.Selectable[gateway.GatewayAPIClient]
}

func (c *config) ApplyDefaults() {
	if c.Driver == "" {
		c.Driver = "json"
	}
	if c.GatewayEndpoint == "" {
		c.GatewayEndpoint = sharedconf.GetGatewaySVC("")
	}
	if c.UserProviderEndpoint == "" {
		c.UserProviderEndpoint = "localhost:9146"
	}
	// Only use sharedconf.GetGatewaySVC for the gateway endpoint
	c.GatewayEndpoint = sharedconf.GetGatewaySVC(c.GatewayEndpoint)
	// Don't use sharedconf.GetGatewaySVC for the user provider endpoint
	// c.UserProviderEndpoint = sharedconf.GetGatewaySVC(c.UserProviderEndpoint)

	// Set default GRPC client options if not set
	if c.GRPCClientOptions == nil {
		c.GRPCClientOptions = map[string]interface{}{
			"tls_mode": "insecure",
		}
	}
}

func (s *service) Register(ss *grpc.Server) {
	ocmcore.RegisterOcmCoreAPIServer(ss, s)
}

func getShareRepository(c *config) (share.Repository, error) {
	if f, ok := registry.NewFuncs[c.Driver]; ok {
		return f(c.Drivers[c.Driver])
	}
	return nil, errtypes.NotFound(fmt.Sprintf("driver not found: %s", c.Driver))
}

// New creates a new ocm core svc.
func New(m map[string]interface{}, ss *grpc.Server, _ *zerolog.Logger) (rgrpc.Service, error) {
	var c config
	if err := cfg.Decode(m, &c); err != nil {
		return nil, err
	}

	c.ApplyDefaults()

	repo, err := getShareRepository(&c)
	if err != nil {
		return nil, err
	}

	// Initialize gateway selector with shared configuration options
	gatewaySelector, err := pool.GatewaySelector(
		c.GatewayEndpoint,
		pool.WithTLSMode(pool.TLSInsecure),
	)
	if err != nil {
		return nil, err
	}

	service := &service{
		conf:            &c,
		repo:            repo,
		gatewaySelector: gatewaySelector,
	}

	return service, nil
}

func (s *service) Close() error {
	return nil
}

func (s *service) UnprotectedEndpoints() []string {
	return []string{
		ocmcore.OcmCoreAPI_CreateOCMCoreShare_FullMethodName,
		ocmcore.OcmCoreAPI_UpdateOCMCoreShare_FullMethodName,
		ocmcore.OcmCoreAPI_DeleteOCMCoreShare_FullMethodName,
	}
}

// CreateOCMCoreShare is called when an OCM request comes into this reva instance from.
func (s *service) CreateOCMCoreShare(ctx context.Context, req *ocmcore.CreateOCMCoreShareRequest) (*ocmcore.CreateOCMCoreShareResponse, error) {
	if req.ShareType != ocm.ShareType_SHARE_TYPE_USER {
		return nil, errtypes.NotSupported("share type not supported")
	}

	now := &typespb.Timestamp{
		Seconds: uint64(time.Now().Unix()),
	}

	// Use the original context for user resolution
	share, err := s.repo.StoreReceivedShare(ctx, &ocm.ReceivedShare{
		RemoteShareId: req.ResourceId,
		Name:          req.Name,
		Grantee: &providerpb.Grantee{
			Type: providerpb.GranteeType_GRANTEE_TYPE_USER,
			Id: &providerpb.Grantee_UserId{
				UserId: req.ShareWith,
			},
		},
		ResourceType: req.ResourceType,
		ShareType:    req.ShareType,
		Owner:        req.Owner,
		Creator:      req.Sender,
		Protocols:    req.Protocols,
		Ctime:        now,
		Mtime:        now,
		Expiration:   req.Expiration,
		State:        ocm.ShareState_SHARE_STATE_PENDING,
	})
	if err != nil {
		// TODO: identify errors
		return &ocmcore.CreateOCMCoreShareResponse{
			Status: status.NewInternal(ctx, err.Error()),
		}, nil
	}

	return &ocmcore.CreateOCMCoreShareResponse{
		Status:  status.NewOK(ctx),
		Id:      share.Id.OpaqueId,
		Created: share.Ctime,
	}, nil
}

func (s *service) UpdateOCMCoreShare(ctx context.Context, req *ocmcore.UpdateOCMCoreShareRequest) (*ocmcore.UpdateOCMCoreShareResponse, error) {
	grantee := utils.ReadPlainFromOpaque(req.GetOpaque(), "grantee")
	if grantee == "" {
		return nil, errtypes.UserRequired("missing remote user id in a metadata")
	}
	if req == nil || len(req.Protocols) == 0 {
		return nil, errtypes.PreconditionFailed("missing protocols in a request")
	}
	fileMask := &fieldmaskpb.FieldMask{Paths: []string{"protocols"}}

	user := &userpb.User{Id: ocmuser.RemoteID(&userpb.UserId{OpaqueId: grantee})}
	_, err := s.repo.UpdateReceivedShare(ctx, user, &ocm.ReceivedShare{
		Id: &ocm.ShareId{
			OpaqueId: req.GetOcmShareId(),
		},
		Protocols: req.Protocols,
	}, fileMask)
	res := &ocmcore.UpdateOCMCoreShareResponse{}
	if err == nil {
		res.Status = status.NewOK(ctx)
	} else {
		var notFound errtypes.NotFound
		if errors.As(err, &notFound) {
			res.Status = status.NewNotFound(ctx, "remote ocm share not found")
		} else {
			res.Status = status.NewInternal(ctx, "error deleting remote ocm share")
		}
	}
	return res, nil
}

func (s *service) DeleteOCMCoreShare(ctx context.Context, req *ocmcore.DeleteOCMCoreShareRequest) (*ocmcore.DeleteOCMCoreShareResponse, error) {
	grantee := utils.ReadPlainFromOpaque(req.GetOpaque(), "grantee")
	if grantee == "" {
		return nil, errtypes.UserRequired("missing remote user id in a metadata")
	}

	share, err := s.repo.GetReceivedShare(ctx, &userpb.User{Id: ocmuser.RemoteID(&userpb.UserId{OpaqueId: grantee})}, &ocm.ShareReference{
		Spec: &ocm.ShareReference_Id{
			Id: &ocm.ShareId{
				OpaqueId: req.GetId(),
			},
		},
	})
	if err != nil {
		return nil, errtypes.InternalError("unable to get share details")
	}

	granteeUser := &userpb.User{Id: ocmuser.RemoteID(&userpb.UserId{OpaqueId: grantee})}
	sharerUserId := share.GetOwner()
	resourceName := share.Name
	nowInSeconds := uint64(time.Now().Unix())

	err = s.repo.DeleteReceivedShare(ctx, granteeUser, &ocm.ShareReference{
		Spec: &ocm.ShareReference_Id{
			Id: &ocm.ShareId{
				OpaqueId: req.GetId(),
			},
		},
	})

	res := &ocmcore.DeleteOCMCoreShareResponse{}
	if err == nil {
		res.Status = status.NewOK(ctx)
		opaque := utils.AppendPlainToOpaque(nil, "timestamp", fmt.Sprintf("%d", nowInSeconds))
		opaque = utils.AppendPlainToOpaque(opaque, "resourcename", resourceName)
		opaque = utils.AppendJSONToOpaque(opaque, "granteeuserid", granteeUser.Id)
		opaque = utils.AppendJSONToOpaque(opaque, "shareruserid", sharerUserId)
		res.Opaque = opaque
		spew.Dump(res)
	} else {
		var notFound errtypes.NotFound
		if errors.As(err, &notFound) {
			res.Status = status.NewNotFound(ctx, "remote ocm share not found")
		} else {
			res.Status = status.NewInternal(ctx, "error deleting remote ocm share")
		}
	}
	return res, nil
}

// Helper function to get authenticated context
func (s *service) getAuthenticatedContext(ctx context.Context) (context.Context, error) {
	if s.conf.ServiceAccountID == "" || s.conf.ServiceAccountSecret == "" {
		return nil, fmt.Errorf("service account credentials not configured")
	}

	// Get a gateway client
	gatewayClient, err := s.gatewaySelector.Next()
	if err != nil {
		return nil, fmt.Errorf("failed to get gateway client: %w", err)
	}

	// Get an authenticated context using the service account
	return utils.GetServiceUserContextWithContext(ctx, gatewayClient, s.conf.ServiceAccountID, s.conf.ServiceAccountSecret)
}
