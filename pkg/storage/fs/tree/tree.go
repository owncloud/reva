// Copyright 2018-2019 CERN
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

package tree

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"strings"

	rpcpb "github.com/cs3org/go-cs3apis/cs3/rpc"
	storageproviderv0alphapb "github.com/cs3org/go-cs3apis/cs3/storageprovider/v0alpha"
	storageregistryv0alphapb "github.com/cs3org/go-cs3apis/cs3/storageregistry/v0alpha"
	"github.com/cs3org/reva/pkg/appctx"
	"github.com/cs3org/reva/pkg/storage"
	"github.com/cs3org/reva/pkg/storage/fs/registry"
	"google.golang.org/grpc"

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

func init() {
	registry.Register("tree", New)
}

type config struct {
	Prefix             string `mapstructure:"prefix"`
	StorageRegistrySVC string `mapstructure:"storageregistrysvc"`
}

func parseConfig(m map[string]interface{}) (*config, error) {
	c := &config{}
	if err := mapstructure.Decode(m, c); err != nil {
		err = errors.Wrap(err, "error decoding conf")
		return nil, err
	}
	return c, nil
}

func (c *config) init() {
	if c.StorageRegistrySVC == "" {
		c.StorageRegistrySVC = ":9999"
	}
}

func (s *treeStorage) getRegistryConn() (*grpc.ClientConn, error) {
	// TODO cache connection?
	if s.rConn != nil {
		return s.rConn, nil
	}

	rConn, err := grpc.Dial(s.config.StorageRegistrySVC, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return rConn, nil
}

func (s *treeStorage) getRegistryClient() (storageregistryv0alphapb.StorageRegistryServiceClient, error) {
	if s.rClient != nil {
		return s.rClient, nil
	}

	rConn, err := s.getRegistryConn()
	if err != nil {
		return nil, err
	}
	s.rClient = storageregistryv0alphapb.NewStorageRegistryServiceClient(rConn)
	return s.rClient, nil
}

func (s *treeStorage) getProviderConn(storageProviderSVC string) (*grpc.ClientConn, error) {
	pConn, err := grpc.Dial(storageProviderSVC, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return pConn, nil
}

func (s *treeStorage) getProviderClient(storageProviderSVC string) (storageproviderv0alphapb.StorageProviderServiceClient, error) {
	if s.pClient[storageProviderSVC] != nil {
		return s.pClient[storageProviderSVC], nil
	}

	pConn, err := s.getProviderConn(storageProviderSVC)
	if err != nil {
		return nil, err
	}
	s.pClient[storageProviderSVC] = storageproviderv0alphapb.NewStorageProviderServiceClient(pConn)
	return s.pClient[storageProviderSVC], nil
}

// New returns an implementation of the storage.FS interface that is backed by a redis kv store
// TODO use an out of process kv store, eg redis or quarkdb
// the keyspace is divided in several namespaces:
// 1. the n: prefix which contains node metadata under the node id, a uuid
//   - contains dir and file nodes, just as in a posix fs
// 2. the n:<nodeid>:path entries which are used to look up path by fileid
//   - this is cheaper than having to rewrite all metadata at the cost of doubling the amount of keys
// TODO implement 3. the p/ prefix which is used to look up fileid by path
//   - contains path as key
//   - is a performance vs memory tradeof, for now we do multiple lookups
//
// see LocoFS: A Loosely-Coupled Metadata Service for Distributed File Systems http://118.190.133.23/lisiyang.pdf
func New(m map[string]interface{}) (storage.FS, error) {
	c, err := parseConfig(m)
	if err != nil {
		return nil, err
	}
	c.init()

	s := &treeStorage{config: c}

	return s, nil
}

func (s *treeStorage) Shutdown() error {
	return nil
}

func (s *treeStorage) addPrefix(p string) string {
	np := path.Join(s.config.Prefix, p)
	return np
}

func (s *treeStorage) removePrefix(np string) string {
	p := strings.TrimPrefix(np, s.config.Prefix)
	if p == "" {
		p = "/"
	}
	return p
}

type treeStorage struct {
	config  *config
	rConn   *grpc.ClientConn
	rClient storageregistryv0alphapb.StorageRegistryServiceClient
	pClient map[string]storageproviderv0alphapb.StorageProviderServiceClient
}

// calcEtag will create an etag based on the md5 of
// - mtime,
// TODO mtime vs ctime
// - inode (if available),
// - device (if available) and
// - size.
// errors are logged, but an etag will still be returned
func calcEtag(ctx context.Context, md *storage.MD) string {
	log := appctx.GetLogger(ctx)
	h := md5.New()
	err := binary.Write(h, binary.BigEndian, md.Mtime.Seconds)
	if err != nil {
		log.Error().Err(err).Msg("error writing mtime seconds")
	}
	err = binary.Write(h, binary.BigEndian, md.Mtime.Nanos)
	if err != nil {
		log.Error().Err(err).Msg("error writing mtime nanos")
	}
	err = binary.Write(h, binary.BigEndian, md.Size)
	if err != nil {
		log.Error().Err(err).Msg("error writing size")
	}
	return fmt.Sprintf(`"%x"`, h.Sum(nil))
}

/*****************************************************************************
 * This is where the actual storage interface starts to get implemented
 *****************************************************************************/

// GetMD is the first call that the storageprovidersvc makes to determine if a file or folder exists
// It is also used to fetch the metadata for a PROPFIND on the root resource, which is why we need to
// propagate changes up to the root.
func (s *treeStorage) GetMD(ctx context.Context, fn string) (*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	path := s.addPrefix(fn)

	log.Debug().
		Str("path", path).
		Interface("ctx", ctx).
		Msg("GetMD")

	rClient, err := s.getRegistryClient()
	if err != nil {
		log.Error().Err(err).Msg("error getting grpc registry client")
		return nil, err
	}

	/*
		res, err := client.ListStorageProviders(ctx, &storageregistryv0alphapb.ListStorageProvidersRequest{})
		if err != nil {
			log.Error().Err(err).Msg("error sending a grpc stat request")
			return nil, err
		}
	*/
	// now we have a list of storage providers ... we can build a tree with that
	// TODO no we want to use get storageprovider for a certain path ... and then delegate the request to that storageprovider
	// - kind of a reverse overlay fs ... at the bottom we have a kv store backed fs which only holds metadata
	// - not at the bottom, but at the root
	// - the root tree is used to aggregate metadata / mtime and etag changes
	// - the tree coordinates when to use which storage and also propagation across storage boundaries?
	// -

	// what about shares?
	// - the share providers would need to be queried one by one to get a list of all shares
	//   - a share has a ResourceId, pointing to a storage and an opaque id ... which causes the logic to search for
	//     the storage and then resolve the opaque id as the path so we can find the mount point in the tree ...
	//     something that needs caching
	//   - this list of share mount points is an overlay to an actual storage, because users assume they can move
	//     or organize shared files in a virtual tree as they see fit
	//   - it means users might want to see shares in multiple trees (use links?)
	// - a storage cannot list these mounted shares ... we need a tree for that
	//   - but how can the propagation logic and the direntry / inode disconnect be solved?
	//     - for propagation this tree storage needs to maintain the metadata for every mount point and all
	//       intermediate nodes up to the root, so we can propagat to the root
	//     - a direntry / inode disconnect happens when a file needs to be added to the root of a mount point
	//       Say we have /aliya_abernathy/ as the root storage and /aliya_abernathy/someproject as a mounted share.
	//       When a file is PUT to /aliya_abernathy/someproject/readme.md then the direntry needs to be added to the
	//       /aliya_abernathy/someproject inode. this should happen in the storageprovidersvcs drivet. The metadata
	//       for the file needs to be stored to the inote which is done by the datesvcs driver, which might act on a
	//       completely different storage. This means the storageprovidersvc might not pick up files that have been
	//       added with the datasvc
	//       - the tree needs to be notified of changes
	//         - this might solve the direntry / inode disconnect when the storage tells the storageprovider, hey there is a new file here, now
	//   - the shareprovider registry seems to consider only a single instance of a share provider
	//     - if multiple storages are used the share provider needs to be able to manage shares on all these storages,
	//        which might be done in different ways. easiest way to allow multiple providers?
	//        - no the storage interface has methods to manage permissions ... hm AddGrant, ListGrants, UpdateGrant, RemoveGrant
	//          - so the share registry / providers only query the actual storages and can be used to cache and aggregate the queries ... ok
	//          - but that does not solve the virtual mount points of shares from another storage ... we still need the tree
	ref := &storageproviderv0alphapb.Reference{
		Spec: &storageproviderv0alphapb.Reference_Path{Path: fn},
	}
	res, err := rClient.GetStorageProvider(ctx, &storageregistryv0alphapb.GetStorageProviderRequest{
		Ref: ref,
	})
	if err != nil {
		log.Error().Err(err).Msg("error sending a grpc get storage provider request")
		return nil, err
	}

	pClient, err := s.getProviderClient(res.Provider.Address)
	if err != nil {
		log.Error().Err(err).Msg("error getting grpc provider client")
		return nil, err
	}
	req := &storageproviderv0alphapb.StatRequest{Ref: ref}
	res2, err := pClient.Stat(ctx, req)
	if err != nil {
		log.Error().Err(err).Msg("error sending a grpc stat request")
		return nil, err
	}

	if res.Status.Code != rpcpb.Code_CODE_OK {
		if res.Status.Code == rpcpb.Code_CODE_NOT_FOUND {
			return nil, notFoundError(fn)
		}
		return nil, err
	}

	md := &storage.MD{
		ID:   fmt.Sprintf("%s:%s", res2.Info.Id.StorageId, res2.Info.Id.OpaqueId),
		Etag: res2.Info.Etag,
		Size: res2.Info.Size,
		Path: fn,
	}

	return md, nil
}

func (s *treeStorage) ListFolder(ctx context.Context, fn string) ([]*storage.MD, error) {
	return nil, notSupportedError("op not supported")
}

// GetPathByID returns the path for the given file id
func (s *treeStorage) GetPathByID(ctx context.Context, id string) (string, error) {
	return "", notSupportedError("op not supported")
}

func (s *treeStorage) CreateDir(ctx context.Context, fn string) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) Delete(ctx context.Context, fn string) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) Move(ctx context.Context, oldName string, newName string) error {
	return notSupportedError("op not supported")
}

// Upload creates an entry in the dir listing, but the actual storage of the data needs to be handled elsewhere
// TODO implement overlay storage that storas the data ...
func (s *treeStorage) Upload(ctx context.Context, fn string, r io.ReadCloser) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) Download(ctx context.Context, fn string) (io.ReadCloser, error) {
	return nil, notSupportedError("download")
}

func (s *treeStorage) AddGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) ListGrants(ctx context.Context, path string) ([]*storage.Grant, error) {
	return nil, notSupportedError("op not supported")
}

func (s *treeStorage) RemoveGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) UpdateGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *treeStorage) GetQuota(ctx context.Context) (int, int, error) {
	return 0, 0, nil
}
func (s *treeStorage) ListRevisions(ctx context.Context, path string) ([]*storage.Revision, error) {
	return nil, notSupportedError("list revisions")
}

func (s *treeStorage) DownloadRevision(ctx context.Context, path, revisionKey string) (io.ReadCloser, error) {
	return nil, notSupportedError("download revision")
}

func (s *treeStorage) RestoreRevision(ctx context.Context, path, revisionKey string) error {
	return notSupportedError("restore revision")
}

func (s *treeStorage) EmptyRecycle(ctx context.Context, path string) error {
	return notSupportedError("empty recycle")
}

func (s *treeStorage) ListRecycle(ctx context.Context, path string) ([]*storage.RecycleItem, error) {
	return nil, notSupportedError("list recycle")
}

func (s *treeStorage) RestoreRecycleItem(ctx context.Context, fn, restoreKey string) error {
	return notSupportedError("restore recycle")
}

type notSupportedError string
type notFoundError string

func (e notSupportedError) Error() string   { return string(e) }
func (e notSupportedError) IsNotSupported() {}
func (e notFoundError) Error() string       { return string(e) }
func (e notFoundError) IsNotFound()         {}
