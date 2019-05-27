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

// Options for storing a filesystem in a kv store:
// 1. s3 uses the path as the key. That leads to excessive copying of all children of a moved / renamed folder
// 2. flattening the nodes to look them up by fileid
//    - separates the file metadata from the tree metadata
//    - flatten the tree? use "<parentid>:<fileid>" as key? similar to storing a direntry in a huge node. but now we can iterate over the directory
//      - why not "<parentid>/<filename>" as key?
//        - at least then they are ordered by name ... probably
//        - it would allow directory traversal on key lookups alone!
//        - separates dir metadata from dir entries.
//        - "<parentid>/<filename>" points to a node id
//

// TODO rename badger to virtual.go storage -> actually this is only the tree
//      - can we implement this as a combined storage (overlay may be clearer) that uses two storage implementations, one for metadata, one for data and a sync strategy?
//        - could then be used to migrate storages?
//        - if a storage does not support up / download it will use the other
//          - can determine the path using the sync strategy
//      - can combine a badger based fs with a mount point storage to propagate etag/mtime to tho webdav root
//        - needs an endpoint to trigger updates or listen for changes? well, up to the mount point storage implementation
//      - implement a badger fs that returns notSupportedError for up and download
//        - how to deal with recursive delete? the combined storage would have to list all files before trying to delete them in both storages
//		    - can this be sped up with a move to trash?
//   			- needs a tombstone in the upper storage to prevent the overlay storage from going to the underlying storage.
//                - could be stored in the overlay storage? until both storages have deleted / moved the file to trash
// TODO extract metadata store as configurable implementation, one of them being badger, another being redis?
// TODO add data storage as configurable implementation, could be the local, s3 or even the eos implementation? 1:1 mapping or a tree?
// TODO add a sync strategy as configurable implementation
//      - it is used to determine the name on the data storage, eg a path (for s3), the nodeid (for exclusive storage) or even a content hash as name
//      - can be used to determine names for trash and versions?
//      - can use no metadata sync, a notification based sync or a periodic polling to pick up changes from the data storage?
//      - this is our old filecache and it brings the same problems ... why do we need it?
//        - it is used to tie several data storages together under a virtual tree?
//        - but this is only necessary for webdav and mtime propagation / calculation of the root?
//          - 1. we need it to propagate the mtime and store the etag for storages that don't support it, eg minio has no metadata for folders
//            - the minio case maps nicely to this virtual storage, because it can be used as the metadata storage for folders
//          - 2. we need a way to to calculate and cache the root tag for webdav requests
//            - in this case the storage that is being cached does not reflect a storage, but the mountpoints
//
// TODO The blocker is that the badger db instance needs to be accessed from the http datasvc as well as the grpc storageprovidersvc.
//      This causes problems when the two run as separate services because badger locks its data directory. To start an internal
//      badger service in reva that can be used by different svcs we need to introduce a new type of service.
//      But even then the concept of having a datasvc seperate from a storageprovidersvc requires a common metadata storage, otherwise
//      a new file added via a PUT to the datasvc will never show up in the file listing of the storageprovider. especially since webdav
//      requires this to be atomic (does it? well it locks the folder ... so ... urgh: locking also requires a common backend, otherwise
//      the datasvc might PUT a file when the dir has been locked)

package redis

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/cs3org/reva/pkg/appctx"
	"github.com/cs3org/reva/pkg/mime"
	"github.com/cs3org/reva/pkg/storage"
	"github.com/cs3org/reva/pkg/storage/fs/registry"
	"github.com/gofrs/uuid"

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"

	"github.com/gomodule/redigo/redis"
)

func init() {
	registry.Register("redis", New)
}

type config struct {
	Prefix          string `mapstructure:"prefix"`
	Host            string `mapstructure:"host"`
	AutocreateDepth int    `mapstructure:"autocreate_depth"`
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
	if c.Host == "" {
		c.Host = ":6379"
	}
}

// New returns an implementation of the storage.FS interface that is backed by a redis kv store
// the keyspace is divided in several namespaces:
// 1. the n: prefix which contains node metadata under the node id, a uuid
//   - contains dir and file nodes, just as in a posix fs
// 2. the n:<nodeid>:path entries which are used to look up path by fileid
//   - this is cheaper than having to rewrite all metadata at the cost of doubling the amount of keys
// TODO implement 3. the p: prefix to look up fileid by path
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

	pool := &redis.Pool{

		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,

		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", c.Host)
			if err != nil {
				return nil, err
			}
			return c, err
		},

		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}

	s := &redisStorage{config: c, pool: pool}

	return s, nil
}

func (s *redisStorage) Shutdown() error {
	return s.pool.Close()
}

func (s *redisStorage) addPrefix(p string) string {
	np := path.Join(s.config.Prefix, p)
	return np
}

func (s *redisStorage) removePrefix(np string) string {
	p := strings.TrimPrefix(np, s.config.Prefix)
	if p == "" {
		p = "/"
	}
	return p
}

type redisStorage struct {
	pool   *redis.Pool
	config *config
}

// calcEtag will create an etag based on the md5 of
// - mtime,
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

func (s *redisStorage) storeMD(ctx context.Context, parentID string, md *storage.MD) error {
	log := appctx.GetLogger(ctx)
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	name := path.Base(md.Path)
	if name == "/" || name == "." {
		name = ""
	}
	p := md.Path // remember path for path by id lookup
	md.Path = "" // never store path, it is added when reading the md
	if err := e.Encode(md); err != nil {
		log.Error().Err(err)
		return err
	}

	conn := s.pool.Get()
	defer conn.Close()

	//start transaction
	conn.Send("MULTI")

	// 1. add node
	conn.Send("SET", "n:"+md.ID, b.Bytes())

	if name != "" {
		// 2. add dir entry
		conn.Send("SET", "n:"+parentID+":d:"+name, md.ID)
		// 3. update cache for path by id lookup
		conn.Send("SET", "n:"+md.ID+":path", p)
	}
	_, err := conn.Do("EXEC")
	if err != nil {
		return err
	}
	log.Debug().
		Str("parentID", parentID).
		Str("name", name).
		Interface("md", md).
		Msg("stored")
	return nil
}

func (s *redisStorage) autocreate(ctx context.Context, p string) (*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	if s.config.AutocreateDepth >= strings.Count(p, "/") {

		// 1. create root
		segments := strings.Split(p, "/")

		now := time.Now()
		md := &storage.MD{
			ID:          "root",
			Path:        "",
			IsDir:       true,
			Mime:        mime.Detect(true, segments[0]),
			Permissions: &storage.PermissionSet{ListContainer: true, CreateContainer: true},
			Size:        uint64(0),
			Mtime: &storage.Timestamp{
				Seconds: uint64(now.Unix()),
				Nanos:   uint32(now.Nanosecond()),
			},
		}

		log.Debug().
			Interface("md", md).
			Msg("autocreating root node")

		err := s.storeMD(context.Background(), "", md)
		if err != nil {
			return nil, err
		}

		parentID := "root"
		p := "/"
		for i := 1; i < len(segments); i++ {
			id := uuid.Must(uuid.NewV4())
			p = path.Join(p, segments[i])

			// 2. create node
			md = &storage.MD{
				ID:          id.String(),
				Path:        p,
				IsDir:       true,
				Mime:        mime.Detect(true, segments[i]),
				Permissions: &storage.PermissionSet{ListContainer: true, CreateContainer: true},
				Size:        uint64(0),
				Mtime: &storage.Timestamp{
					Seconds: uint64(now.Unix()),
					Nanos:   uint32(now.Nanosecond()),
				},
			}

			log.Debug().
				Interface("md", md).
				Str("parentID", parentID).
				Str("segment", segments[i]).
				Msg("autocreating node")

			err = s.storeMD(ctx, parentID, md)
			if err != nil {
				return nil, err
			}

			parentID = md.ID
		}
		return md, err
	}

	log.Debug().
		Str("p", p).
		Msg("not autocreating")
	return nil, nil
}

// getNodeByPath returns the parent id and the metadata for the node of the given path
// The parent id is a byprodoct of the tree traversal and is used to save a lookup when executing a Move()
func (s *redisStorage) getNodeByPath(ctx context.Context, fn string) (string, *storage.MD, error) {
	log := appctx.GetLogger(ctx)
	p := path.Clean("/" + fn)
	var segments []string
	if p == "/" {
		segments = nil
	} else {
		segments = strings.Split(p, "/")
	}

	// we start at the root node
	nodeID := "root"
	parentID := ""

	md := &storage.MD{}

	conn := s.pool.Get()
	defer conn.Close()

	// if we have segments traverse the tree
	for i := 1; i < len(segments); i++ {
		k := "n:" + nodeID + ":d:" + segments[i]
		log.Debug().Str("key", k).Msg("lookup")
		parentID = nodeID
		// lookup the dir entry
		result, err := redis.String(conn.Do("GET", k))
		if err != nil {
			return "", nil, notFoundError(fn)
		}
		nodeID = result
		log.Debug().Str("key", k).Str("nodeID", nodeID).Str("segment", segments[i]).Msg("resolved dir entry")
	}

	// now get the final node
	k := "n:" + nodeID
	data, err := redis.Bytes(conn.Do("GET", k))
	if err != nil {
		log.Error().Str("key", k).
			Err(err)
		return "", nil, notFoundError(fn)
	}
	log.Debug().Str("key", k).Interface("data", data).Msg("got data")
	d := gob.NewDecoder(bytes.NewReader(data))
	err = d.Decode(&md)

	if err != nil {
		log.Error().
			Err(err)
		return "", nil, err
	}
	md.Path = fn

	log.Debug().Str("parentID", parentID).Interface("md", md).Msg("got metadata")
	return parentID, md, nil
}

/*
// propagate mtime, etag and size?
func (s *redisStorage) propagate(ctx context.Context, md *storage.MD) error {
	log := appctx.GetLogger(ctx)
	// split path into segments
	parent := path.Dir(md.Path)
	isRoot := false

	pMD := &storage.MD{}

	// TODO we could do this in a custom built transaction and update all parents at once.
	// if a parent changed we should be able to ignore the error because in that case we neither
	// need to update the mtime nor the etag of the parent.
	err := s.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(parent))
		if err != nil {
			return err
		}

		if item.UserMeta()&metaRoot != 0 {
			isRoot = true
		}

		val, err := item.Value()
		if err != nil {
			return err
		}
		d := gob.NewDecoder(bytes.NewReader(val))
		err = d.Decode(&pMD)
		if err != nil {
			return err
		}

		// propagate mtime if newer
		// TODO what about touching with a past date? covered by mtime?
		if pMD.Mtime.Seconds < md.Mtime.Seconds {
			pMD.Mtime.Seconds = md.Mtime.Seconds
			pMD.Mtime.Nanos = md.Mtime.Nanos
		} else if pMD.Mtime.Seconds == md.Mtime.Seconds && pMD.Mtime.Nanos < md.Mtime.Nanos {
			pMD.Mtime.Nanos = md.Mtime.Nanos
		}

		// always propagate etag
		pMD.Etag = md.Etag

		var b bytes.Buffer
		e := gob.NewEncoder(&b)
		if err := e.Encode(pMD); err != nil {
			log.Error().Err(err)
			return err
		}
		return txn.Set([]byte(parent), b.Bytes())
	})
	if err != nil {
		log.Error().Err(err)
		return err
	}
	// stop propagating at root
	if !isRoot {
		err = s.propagate(ctx, pMD)
	}
	return err
}
*/

// GetPathByID returns the path for the given file id
func (s *redisStorage) GetPathByID(ctx context.Context, id string) (string, error) {
	conn := s.pool.Get()
	defer conn.Close()
	p, err := redis.String(conn.Do("GET", "n:"+id+":path"))
	if err != nil {
		return "", notFoundError(id)
	}
	return p, nil
}

func (s *redisStorage) AddGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *redisStorage) ListGrants(ctx context.Context, path string) ([]*storage.Grant, error) {
	return nil, notSupportedError("op not supported")
}

func (s *redisStorage) RemoveGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *redisStorage) UpdateGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *redisStorage) GetQuota(ctx context.Context) (int, int, error) {
	return 0, 0, nil
}

func (s *redisStorage) CreateDir(ctx context.Context, fn string) error {
	log := appctx.GetLogger(ctx)
	p := s.addPrefix(fn)

	log.Debug().
		Str("path", p).
		Interface("ctx", ctx).
		Msg("CreateDir")

	_, parent, err := s.getNodeByPath(ctx, path.Dir(p))
	if err != nil {
		return err
	}

	now := time.Now()
	id := uuid.Must(uuid.NewV4())
	md := &storage.MD{
		ID:          id.String(),
		Path:        p,
		IsDir:       true,
		Mime:        mime.Detect(true, p),
		Permissions: &storage.PermissionSet{ListContainer: true, CreateContainer: true},
		Size:        uint64(0),
		Mtime: &storage.Timestamp{
			Seconds: uint64(now.Unix()),
			Nanos:   uint32(now.Nanosecond()),
		},
	}
	md.Etag = calcEtag(ctx, md)
	err = s.storeMD(ctx, parent.ID, md)
	if err != nil {
		return err
	}
	//s.propagate(ctx, md)

	return err
}

func (s *redisStorage) Delete(ctx context.Context, fn string) error {
	log := appctx.GetLogger(ctx)
	p := s.addPrefix(fn)

	log.Debug().
		Str("path", p).
		Interface("ctx", ctx).
		Msg("Delete")

	// get old node and parent id
	parentID, n, err := s.getNodeByPath(ctx, p)
	if err != nil {
		return err
	}
	// TODO delete trees recursively
	//      - needs access to actual storage to delete blobs?

	conn := s.pool.Get()
	defer conn.Close()

	// 1. add node
	_, err = conn.Do("DEL", "n:"+parentID+":d:"+path.Base(p), "n:"+n.ID, "n:"+n.ID+":path")

	// TODO propagate

	return err
}

func (s *redisStorage) Move(ctx context.Context, oldName string, newName string) error {
	log := appctx.GetLogger(ctx)
	oldPath := s.addPrefix(oldName)
	newPath := s.addPrefix(newName)

	log.Debug().
		Str("oldPath", oldPath).
		Str("newPath", newPath).
		Interface("ctx", ctx).
		Msg("Move")

	// get old node and parent id
	oldParentID, node, err := s.getNodeByPath(ctx, oldPath)
	if err != nil {
		return err
	}
	_, newParent, err := s.getNodeByPath(ctx, path.Dir(newPath))
	if err != nil {
		return err
	}

	conn := s.pool.Get()
	defer conn.Close()

	//start transaction
	conn.Do("MULTI")

	// 1. delete old dir entry
	conn.Send("DEL", "n:"+oldParentID+":d:"+path.Base(oldPath))

	// 2. add dir entry
	conn.Send("SET", "n:"+newParent.ID+":d:"+path.Base(newPath), node.ID)

	// 3. update cache for path by id lookup
	conn.Send("SET", "n:"+node.ID+":path", newPath)

	// TODO implement recursive path update for all children

	_, err = conn.Do("EXEC")

	// TODO propagate

	return err
}

func (s *redisStorage) GetMD(ctx context.Context, fn string) (*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	path := s.addPrefix(fn)

	log.Debug().
		Str("path", path).
		Interface("ctx", ctx).
		Msg("GetMD")

	_, md, err := s.getNodeByPath(ctx, path)
	if _, ok := err.(notFoundError); ok {
		md = nil
	} else if err != nil {
		return nil, err
	}

	if md != nil {
		log.Debug().
			Interface("md", md).
			Msg("returning metadata")
		return md, nil
	}
	// check if fn is in the autocreate depth
	md, err = s.autocreate(ctx, path)
	if err != nil {
		return nil, err
	}
	if md != nil {
		log.Debug().
			Interface("md", md).
			Msg("returning metadata")
		return md, nil
	}
	return nil, notFoundError(fn)
}

func (s *redisStorage) ListFolder(ctx context.Context, fn string) ([]*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	p := s.addPrefix(fn)

	log.Debug().
		Str("path", p).
		Interface("ctx", ctx).
		Msg("ListFolder")

	_, md, err := s.getNodeByPath(ctx, p)
	if err != nil {
		log.Error().Err(err)
		return nil, err
	}

	finfos := []*storage.MD{}

	parentID := md.ID

	conn := s.pool.Get()
	defer conn.Close()

	prefix := "n:" + parentID + ":d:"
	pattern := prefix + "*"
	prefixLength := len(prefix)

	iter := 0
	for {
		arr, err := redis.Values(conn.Do("SCAN", iter, "MATCH", pattern))
		if err != nil {
			return nil, fmt.Errorf("error retrieving '%s' keys", pattern)
		}

		iter, _ = redis.Int(arr[0], nil)
		keys, _ := redis.Strings(arr[1], nil)
		for _, k := range keys {
			name := k[prefixLength:]
			log.Debug().
				Str("key", k).
				Str("name", name).
				Msg("found dir entry")
			// remember file name which is part of the key
			nodeID, err := redis.String(conn.Do("GET", k))
			if err != nil {
				log.Error().Err(err).Str("key", k).Msg("could not read dir entry found")
				continue // if there is an error we still want to see the rest of the files
			}
			node, err := redis.Bytes(conn.Do("GET", "n:"+nodeID))
			if err != nil {
				log.Error().Err(err).Str("nodeID", nodeID).Msg("could not read node")
				continue // if there is an error we still want to see the rest of the files
			}

			md := &storage.MD{}

			d := gob.NewDecoder(bytes.NewReader(node))
			err = d.Decode(&md)
			if err != nil {
				log.Error().Err(err).Str("nodeID", nodeID).Msg("could not decode node")
				continue
			}

			md.Path = path.Join(fn, name)

			log.Debug().
				Str("name", name).
				Interface("md", md).
				Msg("adding entry")

			finfos = append(finfos, md)
		}
		if iter == 0 {
			break
		}
	}

	log.Debug().
		Int("count", len(finfos)).
		Msg("returning results")
	return finfos, nil
}

// Upload creates an entry in the dir listing, but the actual storage of the data needs to be handled elsewhere
// TODO implement overlay storage that storas the data ...
func (s *redisStorage) Upload(ctx context.Context, fn string, r io.ReadCloser) error {

	log := appctx.GetLogger(ctx)
	p := s.addPrefix(fn)

	log.Debug().
		Str("path", p).
		Interface("ctx", ctx).
		Msg("Upload")

	_, parent, err := s.getNodeByPath(ctx, path.Dir(p))
	if err != nil {
		return err
	}

	now := time.Now()
	id := uuid.Must(uuid.NewV4())
	name := path.Base(p)
	md := &storage.MD{
		ID:          id.String(),
		Path:        p,
		IsDir:       false,
		Mime:        mime.Detect(true, name),
		Permissions: &storage.PermissionSet{ListContainer: false, CreateContainer: false},
		Size:        uint64(0), // TODO ... this is wrong .. how do we get the correct size? Update after writing? which means the overlay needs to see the propagation
		Mtime: &storage.Timestamp{
			Seconds: uint64(now.Unix()),
			Nanos:   uint32(now.Nanosecond()),
		},
	}

	// 2. create node

	log.Debug().
		Interface("md", md).
		Str("parentID", parent.ID).
		Msg("creating file node")

	err = s.storeMD(ctx, parent.ID, md)

	if err != nil {
		return err
	}
	//s.propagate(ctx, md)

	// TODO should we return the ID? so the overlay can store the data under the id instead of the path?
	// we don't even have any metadata for the node? no size ... nothing. should we store that or is it provided by the lower storage?
	return err
}

func (s *redisStorage) Download(ctx context.Context, fn string) (io.ReadCloser, error) {
	return nil, notSupportedError("download")
}

func (s *redisStorage) ListRevisions(ctx context.Context, path string) ([]*storage.Revision, error) {
	return nil, notSupportedError("list revisions")
}

func (s *redisStorage) DownloadRevision(ctx context.Context, path, revisionKey string) (io.ReadCloser, error) {
	return nil, notSupportedError("download revision")
}

func (s *redisStorage) RestoreRevision(ctx context.Context, path, revisionKey string) error {
	return notSupportedError("restore revision")
}

func (s *redisStorage) EmptyRecycle(ctx context.Context, path string) error {
	return notSupportedError("empty recycle")
}

func (s *redisStorage) ListRecycle(ctx context.Context, path string) ([]*storage.RecycleItem, error) {
	return nil, notSupportedError("list recycle")
}

func (s *redisStorage) RestoreRecycleItem(ctx context.Context, fn, restoreKey string) error {
	return notSupportedError("restore recycle")
}

type notSupportedError string
type notFoundError string

func (e notSupportedError) Error() string   { return string(e) }
func (e notSupportedError) IsNotSupported() {}
func (e notFoundError) Error() string       { return string(e) }
func (e notFoundError) IsNotFound()         {}
