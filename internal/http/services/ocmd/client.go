// Copyright 2018-2025 CERN
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

package ocmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cs3org/reva/v2/internal/http/services/wellknown"
	"github.com/cs3org/reva/v2/pkg/appctx"
	"github.com/cs3org/reva/v2/pkg/errtypes"
	"github.com/pkg/errors"
)

// OCMClient is the client for an OCM provider.
type OCMClient struct {
	client *http.Client
}

// NewClient returns a new OCMClient.
func NewClient(timeout time.Duration, insecure bool) *OCMClient {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
	}
	return &OCMClient{
		client: &http.Client{
			Transport: tr,
			Timeout:   timeout,
		},
	}
}

// Discover returns the OCM discovery information for a remote endpoint.
// It tries /.well-known/ocm first, then falls back to /ocm-provider (legacy).
// https://cs3org.github.io/OCM-API/docs.html?branch=develop&repo=OCM-API&user=cs3org#/paths/~1ocm-provider/get
func (c *OCMClient) Discover(ctx context.Context, endpoint string) (*wellknown.OcmDiscoveryData, error) {
	log := appctx.GetLogger(ctx)

	remoteurl, _ := url.JoinPath(endpoint, "/.well-known/ocm")
	body, err := c.discover(ctx, remoteurl)
	if err != nil || len(body) == 0 {
		log.Debug().Err(err).Str("sender", remoteurl).Str("response", string(body)).
			Msg("invalid or empty response, falling back to legacy discovery")
		remoteurl, _ := url.JoinPath(endpoint, "/ocm-provider") // legacy discovery endpoint

		body, err = c.discover(ctx, remoteurl)
		if err != nil || len(body) == 0 {
			log.Warn().Err(err).Str("sender", remoteurl).Str("response", string(body)).
				Msg("invalid or empty response")
			return nil, errtypes.BadRequest("Invalid response on OCM discovery")
		}
	}

	var disco wellknown.OcmDiscoveryData
	err = json.Unmarshal(body, &disco)
	if err != nil {
		log.Warn().Err(err).Str("sender", remoteurl).Str("response", string(body)).
			Msg("malformed response")
		return nil, errtypes.BadRequest("Invalid payload on OCM discovery")
	}

	log.Debug().Str("sender", remoteurl).Any("response", disco).Msg("discovery response")
	return &disco, nil
}

func (c *OCMClient) discover(ctx context.Context, url string) ([]byte, error) {
	log := appctx.GetLogger(ctx)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "error creating OCM discovery request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "error doing OCM discovery request")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn().Str("sender", url).Int("status", resp.StatusCode).Msg("discovery returned")
		return nil, errtypes.BadRequest("Remote does not offer a valid OCM discovery endpoint")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "malformed remote OCM discovery")
	}
	return body, nil
}
