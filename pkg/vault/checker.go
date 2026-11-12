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
	"time"

	"github.com/bluele/gcache"
	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
)

const (
	tracerName = "github.com/owncloud/reva/v2/pkg/vault"

	// defaultConcurrency limits how many checks FilterUsers runs at the same time,
	// so that a large group does not send all its checks to the gateway at once.
	defaultConcurrency = 10
)

// ClientFunc returns the client for one check. The Checker calls it for each check,
// so that the client can come from a selector.
type ClientFunc func() (PermissionClient, error)

// SelectorClient returns a ClientFunc that takes the next gateway client from the selector.
func SelectorClient(selector pool.Selectable[gateway.GatewayAPIClient]) ClientFunc {
	return func() (PermissionClient, error) {
		return selector.Next()
	}
}

// AddressClient returns a ClientFunc that takes the gateway client for the address from the pool.
func AddressClient(address string) ClientFunc {
	return func() (PermissionClient, error) {
		return pool.GetGatewayServiceClient(address)
	}
}

// Checker checks the VaultMode permission of users.
// A Checker can cache the definitive answers. Each instance has its own cache.
type Checker struct {
	client      ClientFunc
	cache       gcache.Cache
	ttl         time.Duration
	concurrency int
}

// Option configures a Checker.
type Option func(*Checker)

// WithCache caches the definitive answers for each user for the ttl.
// The Checker does not cache when size or ttl is not positive.
func WithCache(size int, ttl time.Duration) Option {
	return func(c *Checker) {
		if size > 0 && ttl > 0 {
			c.cache = gcache.New(size).LRU().Build()
			c.ttl = ttl
		}
	}
}

// WithConcurrency sets how many checks FilterUsers runs at the same time.
// Values that are not positive have no effect.
func WithConcurrency(n int) Option {
	return func(c *Checker) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// NewChecker returns a Checker that gets its client from the ClientFunc.
func NewChecker(client ClientFunc, opts ...Option) *Checker {
	c := &Checker{client: client, concurrency: defaultConcurrency}
	for _, o := range opts {
		o(c)
	}
	return c
}

// HasVaultMode reports whether the user holds the VaultMode permission.
// A non-nil error means undetermined, so callers must treat the user as not allowed.
// The Checker caches only definitive answers.
func (c *Checker) HasVaultMode(ctx context.Context, userID *userpb.UserId) (bool, error) {
	key := userID.GetOpaqueId()
	if c.cache != nil {
		if v, err := c.cache.Get(key); err == nil {
			return v.(bool), nil
		}
	}

	client, err := c.client()
	if err != nil {
		return false, err
	}
	allowed, err := HasVaultMode(ctx, client, userID)
	if err != nil {
		return false, err
	}

	if c.cache != nil {
		_ = c.cache.SetWithExpire(key, allowed, c.ttl)
	}
	return allowed, nil
}

// FilterUsers returns the users from userIDs who hold the VaultMode permission, in the same order.
// A group can contain users without the permission, because the gateway does not check the
// group members. The checks run in parallel, with a limit. When a check fails, FilterUsers
// calls onError for that user and leaves the user out, so that the caller fails closed.
func (c *Checker) FilterUsers(ctx context.Context, userIDs []*userpb.UserId, onError func(*userpb.UserId, error)) []*userpb.UserId {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "FilterUsers",
		trace.WithAttributes(attribute.Int("vault.users.checked", len(userIDs))))
	defer span.End()

	allowed := make([]bool, len(userIDs))
	errs := make([]error, len(userIDs))
	var eg errgroup.Group
	eg.SetLimit(c.concurrency)
	for i, id := range userIDs {
		eg.Go(func() error {
			allowed[i], errs[i] = c.HasVaultMode(ctx, id)
			return nil
		})
	}
	_ = eg.Wait()

	filtered := make([]*userpb.UserId, 0, len(userIDs))
	failed := 0
	for i, id := range userIDs {
		if errs[i] != nil {
			failed++
			if onError != nil {
				onError(id, errs[i])
			}
			continue
		}
		if allowed[i] {
			filtered = append(filtered, id)
		}
	}
	span.SetAttributes(
		attribute.Int("vault.users.allowed", len(filtered)),
		attribute.Int("vault.users.failed", failed),
	)
	return filtered
}
