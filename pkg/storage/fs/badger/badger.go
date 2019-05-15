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

package badger

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

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"

	"github.com/dgraph-io/badger"
)

func init() {
	registry.Register("badger", New)
}

type config struct {
	// Dir will be created if it does not exist
	Dir             string `mapstructure:"dir"`
	ValueDir        string `mapstructure:"value_dir"`
	Prefix          string `mapstructure:"prefix"`
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
	c.Dir = path.Clean(c.Dir)
	if c.Dir == "." {
		c.Dir = "/tmp/badger"
	}
	c.ValueDir = path.Clean(c.ValueDir)
	if c.ValueDir == "." {
		c.ValueDir = "/tmp/badger"
	}
}

// New returns an implementation to of the storage.FS interface that talk to
// a s3 api.
func New(m map[string]interface{}) (storage.FS, error) {
	c, err := parseConfig(m)
	if err != nil {
		return nil, err
	}
	c.init()

	opts := badger.DefaultOptions
	opts.Dir = c.Dir
	opts.ValueDir = c.ValueDir
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	s := &badgerStorage{config: c, db: db}

	return s, nil
}

func (s *badgerStorage) Shutdown() error {
	return s.db.Close()
}

func (s *badgerStorage) addPrefix(p string) string {
	np := path.Join(s.config.Prefix, p)
	return np
}

func (s *badgerStorage) removePrefix(np string) string {
	p := strings.TrimPrefix(np, s.config.Prefix)
	if p == "" {
		p = "/"
	}
	return p
}

type badgerStorage struct {
	db     *badger.DB
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

var metaNode = byte(0x0)
var metaRoot = byte(0x1)

func (s *badgerStorage) storeMD(ctx context.Context, md *storage.MD, meta byte) error {
	log := appctx.GetLogger(ctx)
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(md); err != nil {
		log.Error().Err(err)
		return err
	}
	err := s.db.Update(func(txn *badger.Txn) error {
		return txn.SetWithMeta([]byte(path.Clean(md.Path)), b.Bytes(), meta)
	})
	if err != nil {
		log.Error().Err(err)
	}
	return err
}

// propagate mtime, etag and size?
func (s *badgerStorage) propagate(ctx context.Context, md *storage.MD) error {
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

// GetPathByID returns the path pointed by the file id
// In this implementation the file id is that path of the file without the first slash
// thus the file id always points to the filename
func (s *badgerStorage) GetPathByID(ctx context.Context, id string) (string, error) {
	return path.Join("/", strings.TrimPrefix(id, "fileid-")), nil
}

func (s *badgerStorage) AddGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *badgerStorage) ListGrants(ctx context.Context, path string) ([]*storage.Grant, error) {
	return nil, notSupportedError("op not supported")
}

func (s *badgerStorage) RemoveGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *badgerStorage) UpdateGrant(ctx context.Context, path string, g *storage.Grant) error {
	return notSupportedError("op not supported")
}

func (s *badgerStorage) GetQuota(ctx context.Context) (int, int, error) {
	return 0, 0, nil
}

func (s *badgerStorage) CreateDir(ctx context.Context, fn string) error {

	log := appctx.GetLogger(ctx)
	fn = s.addPrefix(fn)

	log.Debug().
		Str("fn", fn).
		Interface("ctx", ctx).
		Msg("CreateDir")

	now := time.Now()
	md := &storage.MD{
		ID:          "fileid-" + strings.TrimPrefix(fn, "/"), // TODO this breaks ids on rename, generate uuid, store in cache ... hm then it is no longer a cache
		Path:        fn,
		IsDir:       true,
		Mime:        mime.Detect(true, fn),
		Permissions: &storage.PermissionSet{ListContainer: true, CreateContainer: true},
		Size:        uint64(0),
		Mtime: &storage.Timestamp{
			Seconds: uint64(now.Unix()),
			Nanos:   uint32(now.Nanosecond()),
		},
	}
	md.Etag = calcEtag(ctx, md)
	err := s.storeMD(ctx, md, metaNode)
	s.propagate(ctx, md)

	return err
}

func (s *badgerStorage) Delete(ctx context.Context, fn string) error {
	log := appctx.GetLogger(ctx)
	key := s.addPrefix(fn)

	log.Debug().
		Str("key", key).
		Interface("ctx", ctx).
		Msg("Delete")

	err := s.db.Update(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(key))
		if err == badger.ErrKeyNotFound {
			return notFoundError(key)
		}
		if err != nil {
			return err
		}
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // we need to seek keys as fast as possible
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte(key + "/") // we add a slash so we only list the files IN the folder, excluding the folder itself
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			err := txn.Delete(it.Item().Key())
			if err != nil {
				return err
			}
		}
		// and delete the actual key
		return txn.Delete([]byte(key))
	})

	return err
}

func (s *badgerStorage) moveObject(ctx context.Context, oldKey string, newKey string) error {
	/*
		// Copy
		// TODO double check CopyObject can deal with >5GB files.
		// Docs say we need to use multipart upload: https://docs.aws.amazon.com/AmazonS3/latest/API/RESTObjectCOPY.html
		_, err := s.db.CopyObject(&s3.CopyObjectInput{
			Bucket:     aws.String(fs.config.Bucket),
			CopySource: aws.String("/" + fs.config.Bucket + oldKey),
			Key:        aws.String(newKey),
		})
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchBucket:
				return notFoundError(oldKey)
			}
			return err
		}
		// TODO cache etag and mtime?

		// Delete
		_, err = s.db.DeleteObject(&s3.DeleteObjectInput{
			Bucket: aws.String(fs.config.Bucket),
			Key:    aws.String(oldKey),
		})
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchBucket:
			case s3.ErrCodeNoSuchKey:
				return notFoundError(oldKey)
			}
			return err
		}
	*/
	return nil
}

func (s *badgerStorage) Move(ctx context.Context, oldName, newName string) error {
	return notSupportedError("op not supported")
	/*
		log := appctx.GetLogger(ctx)
		fn := s.addPrefix(oldName)

		// first we need to find out if fn is a dir or a file

		_, err := s.db.HeadObject(&s3.HeadObjectInput{
			Bucket: aws.String(fs.config.Bucket),
			Key:    aws.String(fn),
		})
		if err != nil {
			log.Error().Err(err)
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case s3.ErrCodeNoSuchBucket:
				case s3.ErrCodeNoSuchKey:
					return notFoundError(fn)
				}
			}

			// move directory
			input := &s3.ListObjectsV2Input{
				Bucket: aws.String(fs.config.Bucket),
				Prefix: aws.String(fn + "/"),
			}
			isTruncated := true

			for isTruncated {
				output, err := s.db.ListObjectsV2(input)
				if err != nil {
					return errors.Wrap(err, "s3FS: error listing "+fn)
				}

				for _, o := range output.Contents {
					log.Debug().
						Interface("object", *o).
						Str("fn", fn).
						Msg("found Object")

					err := fs.moveObject(ctx, *o.Key, strings.Replace(*o.Key, fn+"/", newName+"/", 1))
					if err != nil {
						return err
					}
				}

				input.ContinuationToken = output.NextContinuationToken
				isTruncated = *output.IsTruncated
			}
			// ok, we are done
			return nil
		}

		// move single object
		err = fs.moveObject(ctx, fn, newName)
		if err != nil {
			return err
		}
		return nil
	*/
}

func (s *badgerStorage) GetMD(ctx context.Context, fn string) (*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	key := s.addPrefix(fn)

	log.Debug().
		Str("key", key).
		Interface("ctx", ctx).
		Msg("GetMD")

	md := &storage.MD{}

	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.Value()
		if err != nil {
			return err
		}
		d := gob.NewDecoder(bytes.NewReader(val))
		return d.Decode(&md)
	})
	// TODO only log cache errors as level debug. limit error to the ones that are actually an error
	// TODO handle badger.ErrKeyNotFound
	if err != nil {
		log.Error().
			Err(err)
		md = nil
	}

	if md != nil {
		log.Debug().
			Interface("md", md).
			Msg("returning metadata")
		return md, nil
	}
	// check if fn is in the autocreate depth
	md, err = s.autocreate(ctx, fn)
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

func (s *badgerStorage) autocreate(ctx context.Context, fn string) (*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	if s.config.AutocreateDepth >= strings.Count(fn, "/") {
		// TODO create dir on the fly
		
		// create first fn segment as root
		segments := strings.Split(fn, "/")
		
		now := time.Now()
		
		p := s.addPrefix("/"+segments[0])
		md := &storage.MD{
			ID:          "fileid-"+segments[0], // TODO this breaks ids on rename, generate uuid, store in cache ... hm then it is no longer a cache
			Path:        p,
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
		
		err := s.storeMD(context.Background(), md, metaRoot)

		for i := 1; i < len(segments); i++ {
			p = path.Join(p, segments[i])

			md = &storage.MD{
				ID:          "fileid-" + strings.TrimPrefix(p, "/"), // TODO this breaks ids on rename, generate uuid, store in cache ... hm then it is no longer a cache
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
				Msg("autocreating node")

			err = s.storeMD(context.Background(), md, metaNode)
		}
		return md, err
	}
	
	log.Debug().
		Interface("fn", fn).
		Msg("not autocreating")
	return nil, nil
}
// TODO jfd check we return fn in all notfounderrors and not the key. the key might leak info of the underlying fs layout 

func (s *badgerStorage) ListFolder(ctx context.Context, fn string) ([]*storage.MD, error) {
	log := appctx.GetLogger(ctx)
	fn = s.addPrefix(fn)

	finfos := []*storage.MD{}

	log.Debug().
		Str("fn", fn).
		Interface("ctx", ctx).
		Msg("ListFolder")

	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(fn))
		if err == badger.ErrKeyNotFound {
			if s.config.AutocreateDepth > strings.Count(fn, "/") {
				// TODO check if fn is in the autocreate depth
				// TODO create dir on the fly
			}
			return notFoundError(fn)
		}
		if err != nil {
			return err
		}
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false // we need to seek keys as fast as possible
		it := txn.NewIterator(opts)
		defer it.Close()
		prefix := []byte(fn + "/") // we add a slash so we only list the files IN the folder, excluding the folder itself
		depth := bytes.Count(prefix, []byte("/"))
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			if bytes.Count(item.Key(), []byte("/")) > depth {
				continue
			}
			v, err := item.Value()
			if err != nil {
				log.Error().Err(err)
				continue
			}

			md := &storage.MD{}

			d := gob.NewDecoder(bytes.NewReader(v))
			err = d.Decode(&md)
			if err != nil {
				log.Error().Err(err)
				continue
			}

			log.Debug().
				Interface("md", md).
				Msg("adding entry")
			finfos = append(finfos, md)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Debug().
		Int("count", len(finfos)).
		Msg("returning results")
	return finfos, nil
}

func (s *badgerStorage) Upload(ctx context.Context, fn string, r io.ReadCloser) error {
	return notSupportedError("upload")
	/*
		log := appctx.GetLogger(ctx)
		fn = s.addPrefix(fn)

		upParams := &s3manager.UploadInput{
			Bucket: aws.String(fs.config.Bucket),
			Key:    aws.String(fn),
			Body:   r,
		}
		uploader := s3manager.NewUploaderWithClient(fs.client)
		result, err := uploader.Upload(upParams)

		if err != nil {
			log.Error().Err(err)
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case s3.ErrCodeNoSuchBucket:
					return notFoundError(fn)
				}
			}
			return errors.Wrap(err, "s3fs: error creating object "+fn)
		}

		log.Debug().Interface("result", result)
		// TODO cache md & etag
		return nil
	*/
}

func (s *badgerStorage) Download(ctx context.Context, fn string) (io.ReadCloser, error) {
	return nil, notSupportedError("download")
	/*
		log := appctx.GetLogger(ctx)
		fn = s.addPrefix(fn)

		// use GetObject instead of s3manager.Downloader:
		// the result.Body is a ReadCloser, which allows streaming
		// TODO double check we are not caching bytes in memory
		r, err := s.db.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(fs.config.Bucket),
			Key:    aws.String(fn),
		})
		if err != nil {
			log.Error().Err(err)
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case s3.ErrCodeNoSuchBucket:
				case s3.ErrCodeNoSuchKey:
					return nil, notFoundError(fn)
				}
			}
			return nil, errors.Wrap(err, "s3fs: error deleting "+fn)
		}
		return r.Body, nil
	*/
}

func (s *badgerStorage) ListRevisions(ctx context.Context, path string) ([]*storage.Revision, error) {
	return nil, notSupportedError("list revisions")
}

func (s *badgerStorage) DownloadRevision(ctx context.Context, path, revisionKey string) (io.ReadCloser, error) {
	return nil, notSupportedError("download revision")
}

func (s *badgerStorage) RestoreRevision(ctx context.Context, path, revisionKey string) error {
	return notSupportedError("restore revision")
}

func (s *badgerStorage) EmptyRecycle(ctx context.Context, path string) error {
	return notSupportedError("empty recycle")
}

func (s *badgerStorage) ListRecycle(ctx context.Context, path string) ([]*storage.RecycleItem, error) {
	return nil, notSupportedError("list recycle")
}

func (s *badgerStorage) RestoreRecycleItem(ctx context.Context, fn, restoreKey string) error {
	return notSupportedError("restore recycle")
}

type notSupportedError string
type notFoundError string

func (e notSupportedError) Error() string   { return string(e) }
func (e notSupportedError) IsNotSupported() {}
func (e notFoundError) Error() string       { return string(e) }
func (e notFoundError) IsNotFound()         {}
