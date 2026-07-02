// Copyright 2018-2024 CERN
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

package upload

import (
	"context"
	"encoding/json"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// TokenOptions carries the JWT-signing configuration needed to produce transfer
// URLs for the postprocessing service.
type TokenOptions struct {
	DownloadEndpoint     string
	DataGatewayEndpoint  string
	TransferSharedSecret string
	TransferExpires      int64
}

// FileStore is a filesystem-backed SessionStore. Sessions are stored as a pair
// of files under <root>/uploads/:
//
//   - <id>.info  — JSON-encoded tusd.FileInfo
//   - <id>       — staged binary bytes
//
// This is the same on-disk format used by OcisStore so existing sessions
// survive a rolling deploy that switches to FileStore.
type FileStore struct {
	root string
	opts TokenOptions
	log  *zerolog.Logger
}

// FileStoreFromDriverConf builds a FileStore from a reva driver config map.
// Returns nil if the config carries no root path (driver does not support
// coordinated uploads). Each service that mounts the same driver calls this
// independently — the same pattern decomposedfs uses for its Aspects.
func FileStoreFromDriverConf(driverConf map[string]interface{}, log *zerolog.Logger) *FileStore {
	if driverConf == nil {
		return nil
	}

	type driverRootConf struct {
		Root            string `mapstructure:"root"`
		UploadDirectory string `mapstructure:"upload_directory"`
	}
	var rc driverRootConf
	_ = mapstructure.Decode(driverConf, &rc)

	root := rc.UploadDirectory
	if root == "" {
		root = rc.Root
	}
	if root == "" {
		return nil
	}

	type tokenConf struct {
		DownloadEndpoint     string `mapstructure:"download_endpoint"`
		DataGatewayEndpoint  string `mapstructure:"datagateway_endpoint"`
		TransferSharedSecret string `mapstructure:"transfer_shared_secret"`
		TransferExpires      int64  `mapstructure:"transfer_expires"`
	}
	var tc tokenConf
	if tokens, ok := driverConf["tokens"]; ok {
		_ = mapstructure.Decode(tokens, &tc)
	}

	return NewFileStore(root, TokenOptions{
		DownloadEndpoint:     tc.DownloadEndpoint,
		DataGatewayEndpoint:  tc.DataGatewayEndpoint,
		TransferSharedSecret: tc.TransferSharedSecret,
		TransferExpires:      tc.TransferExpires,
	}, log)
}

// NewFileStore creates a FileStore rooted at root.
// root must be on a shared filesystem when multiple pods handle the same space.
func NewFileStore(root string, opts TokenOptions, log *zerolog.Logger) *FileStore {
	return &FileStore{root: root, opts: opts, log: log}
}

// New allocates a fresh session with a new UUID.
func (fs *FileStore) New(_ context.Context) Session {
	return &FileSession{
		store: fs,
		info: tusd.FileInfo{
			ID: uuid.New().String(),
			Storage: map[string]string{
				"Type": "FileStore",
			},
			MetaData: tusd.MetaData{},
		},
	}
}

// Get loads the session with the given id from disk.
func (fs *FileStore) Get(ctx context.Context, id string) (Session, error) {
	infoPath := fileSessionPath(fs.root, id)

	data, err := os.ReadFile(infoPath)
	if err != nil {
		if pathErr, ok := err.(*os.PathError); ok && pathErr.Err == syscall.ESTALE {
			return nil, tusd.ErrNotFound
		}
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, tusd.ErrNotFound
		}
		return nil, err
	}

	var info tusd.FileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	session := &FileSession{store: fs, info: info}

	stat, err := os.Stat(session.binPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, tusd.ErrNotFound
		}
		return nil, err
	}
	session.info.Offset = stat.Size()

	return session, nil
}

// List returns all sessions found under <root>/uploads/*.info.
func (fs *FileStore) List(ctx context.Context) ([]Session, error) {
	infoFiles, err := filepath.Glob(filepath.Join(fs.root, "uploads", "*.info"))
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0, len(infoFiles))
	for _, path := range infoFiles {
		id := strings.TrimSuffix(filepath.Base(path), ".info")
		session, err := fs.Get(ctx, id)
		if err != nil {
			fs.log.Error().Str("path", path).Err(err).Msg("filestore: could not load session")
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}
