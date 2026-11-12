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

// Package vault provides the interceptor that protects the vault storage provider.
// The interceptor runs after the auth interceptor, so the user is in the context.
package vault

import (
	"context"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	"github.com/mitchellh/mapstructure"
	"github.com/owncloud/reva/v2/pkg/appctx"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/rgrpc"
	"github.com/owncloud/reva/v2/pkg/sharedconf"
	vaultpkg "github.com/owncloud/reva/v2/pkg/vault"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// defaultPriority runs the interceptor before the other registered interceptors,
	// so that a denied request does not reach them.
	defaultPriority = 50

	defaultVaultModeCacheSize    = 5000
	defaultVaultModeCacheSeconds = 30

	mfaRequiredMsg       = "MFA required to access vault storage"
	vaultModeRequiredMsg = "vault mode permission required to access vault storage"
)

func init() {
	rgrpc.RegisterUnaryInterceptor("vault", NewUnary)
	rgrpc.RegisterStreamInterceptor("vault", NewStream)
}

type config struct {
	Priority    int    `mapstructure:"priority"`
	GatewayAddr string `mapstructure:"gateway_addr"`
	// MFARequired denies access to users whose token does not carry MFA.
	MFARequired bool `mapstructure:"mfa_required"`
	// VaultModeRequired denies access to users who do not hold the VaultMode permission.
	VaultModeRequired     bool `mapstructure:"vault_mode_required"`
	VaultModeCacheSize    int  `mapstructure:"vault_mode_cache_size"`
	VaultModeCacheSeconds int  `mapstructure:"vault_mode_cache_seconds"`
}

func parseConfig(m map[string]interface{}) (*config, error) {
	c := &config{}
	if err := mapstructure.Decode(m, c); err != nil {
		return nil, errors.Wrap(err, "vault: error decoding conf")
	}
	if c.Priority == 0 {
		c.Priority = defaultPriority
	}
	if c.VaultModeCacheSize == 0 {
		c.VaultModeCacheSize = defaultVaultModeCacheSize
	}
	if c.VaultModeCacheSeconds == 0 {
		c.VaultModeCacheSeconds = defaultVaultModeCacheSeconds
	}
	c.GatewayAddr = sharedconf.GetGatewaySVC(c.GatewayAddr)
	return c, nil
}

// guard holds the configuration and the VaultMode checker of one interceptor.
type guard struct {
	conf    *config
	checker *vaultpkg.Checker
}

func newGuard(m map[string]interface{}, client vaultpkg.ClientFunc) (*guard, error) {
	conf, err := parseConfig(m)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = vaultpkg.AddressClient(conf.GatewayAddr)
	}
	return &guard{
		conf: conf,
		checker: vaultpkg.NewChecker(client,
			vaultpkg.WithCache(conf.VaultModeCacheSize, time.Duration(conf.VaultModeCacheSeconds)*time.Second)),
	}, nil
}

// deny returns the reason to deny the request, or an empty string to allow it.
//
// A request without a user in the context passes. The auth interceptor runs first and
// rejects each protected method without a valid token. Only unprotected methods
// can arrive here without a user, and the vault checks never applied to them.
func (g *guard) deny(ctx context.Context) string {
	u, ok := ctxpkg.ContextGetUser(ctx)
	if !ok || u.GetId().GetType() == userpb.UserType_USER_TYPE_SERVICE {
		return ""
	}
	log := appctx.GetLogger(ctx)

	if g.conf.MFARequired && !ctxpkg.HasMFA(ctx) {
		log.Warn().Str("user_id", u.GetId().GetOpaqueId()).Msg("MFA is required")
		return mfaRequiredMsg
	}
	if g.conf.VaultModeRequired {
		allowed, err := g.checker.HasVaultMode(ctx, u.GetId())
		if err != nil {
			log.Error().Err(err).Str("user_id", u.GetId().GetOpaqueId()).Msg("could not check the vault mode permission")
		}
		if !allowed {
			log.Debug().Str("user_id", u.GetId().GetOpaqueId()).Msg("vault mode permission is required")
			return vaultModeRequiredMsg
		}
	}
	return ""
}

// NewUnary returns a new unary interceptor that denies requests to the vault storage
// from users without MFA or without the VaultMode permission.
func NewUnary(m map[string]interface{}) (grpc.UnaryServerInterceptor, int, error) {
	return newUnary(m, nil)
}

func newUnary(m map[string]interface{}, client vaultpkg.ClientFunc) (grpc.UnaryServerInterceptor, int, error) {
	g, err := newGuard(m, client)
	if err != nil {
		return nil, 0, err
	}
	interceptor := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if msg := g.deny(ctx); msg != "" {
			return denyResponse(ctx, req, info, msg)
		}
		return handler(ctx, req)
	}
	return interceptor, g.conf.Priority, nil
}

// NewStream returns a new stream interceptor that denies requests to the vault storage
// from users without MFA or without the VaultMode permission.
func NewStream(m map[string]interface{}) (grpc.StreamServerInterceptor, int, error) {
	return newStream(m, nil)
}

func newStream(m map[string]interface{}, client vaultpkg.ClientFunc) (grpc.StreamServerInterceptor, int, error) {
	g, err := newGuard(m, client)
	if err != nil {
		return nil, 0, err
	}
	interceptor := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if msg := g.deny(ss.Context()); msg != "" {
			return status.Error(codes.PermissionDenied, msg)
		}
		return handler(srv, ss)
	}
	return interceptor, g.conf.Priority, nil
}
