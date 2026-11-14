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

package decomposedfs

import (
	"context"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"

	"github.com/owncloud/reva/v2/pkg/appctx"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata/prefixes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/utils"
)

var authorAttrs = []string{prefixes.AuthorIDAttr, prefixes.AuthorIDPAttr, prefixes.AuthorTypeAttr}

func isAuthorAttr(name string) bool {
	for _, a := range authorAttrs {
		if a == name {
			return true
		}
	}
	return false
}

// setAuthor records the user in the context as author of the content being written.
// Without a user nothing is written, so no author is claimed.
func setAuthor(ctx context.Context, attrs node.Attributes) {
	u, ok := ctxpkg.ContextGetUser(ctx)
	if !ok || u.GetId().GetOpaqueId() == "" {
		return
	}
	attrs.SetString(prefixes.AuthorIDAttr, u.GetId().GetOpaqueId())
	attrs.SetString(prefixes.AuthorIDPAttr, u.GetId().GetIdp())
	attrs.SetString(prefixes.AuthorTypeAttr, utils.UserTypeToString(u.GetId().GetType()))
}

// revisionAuthorOpaque reads the author of a revision. It returns nil for a
// revision without one, e.g. one created before authors were recorded.
func (fs *Decomposedfs) revisionAuthorOpaque(ctx context.Context, revisionPath string) *typespb.Opaque {
	attrs, err := fs.lu.MetadataBackend().All(ctx, revisionPath)
	if err != nil {
		appctx.GetLogger(ctx).Debug().Err(err).Str("revision", revisionPath).Msg("could not read revision author")
		return nil
	}
	id := string(attrs[prefixes.AuthorIDAttr])
	if id == "" {
		return nil
	}
	return utils.AppendJSONToOpaque(nil, storage.RevisionAuthorOpaqueKey, &userpb.UserId{
		OpaqueId: id,
		Idp:      string(attrs[prefixes.AuthorIDPAttr]),
		Type:     utils.UserTypeMap(string(attrs[prefixes.AuthorTypeAttr])),
	})
}

// dropStaleAuthor removes the author attributes from target that the source does not have.
func (fs *Decomposedfs) dropStaleAuthor(ctx context.Context, sourcePath, targetPath string) error {
	src, err := fs.lu.MetadataBackend().All(ctx, sourcePath)
	if err != nil {
		return err
	}
	dst, err := fs.lu.MetadataBackend().All(ctx, targetPath)
	if err != nil {
		return err
	}
	for _, a := range authorAttrs {
		if _, ok := src[a]; ok {
			continue
		}
		// removing an attribute that is not there fails on the xattr backend
		if _, ok := dst[a]; !ok {
			continue
		}
		if err := fs.lu.MetadataBackend().Remove(ctx, targetPath, a, false); err != nil {
			return err
		}
	}
	return nil
}
