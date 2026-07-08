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
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

func nopLog() *zerolog.Logger {
	l := zerolog.Nop()
	return &l
}

// FileStore.Setup

func TestFileStoreSetup_CreatesUploadsDir(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())

	err := fs.Setup()

	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(root, "uploads"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestFileStoreSetup_Idempotent(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())

	require.NoError(t, fs.Setup())
	assert.NoError(t, fs.Setup())
}

// FileStore.New

func TestFileStoreNew_NonEmptyID(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(context.Background())

	assert.NotEmpty(t, s.ID())
}

func TestFileStoreNew_UniqueIDs(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s1 := fs.New(context.Background())
	s2 := fs.New(context.Background())

	assert.NotEqual(t, s1.ID(), s2.ID())
}

func TestFileStoreNew_StorageTypeIsFileStore(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(context.Background())

	info, err := s.GetInfo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "FileStore", info.Storage["Type"])
}

func TestFileStoreNew_MetaDataIsNotNil(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(context.Background())

	info, err := s.GetInfo(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, info.MetaData)
}

// FileStore.Get

func TestFileStoreGet_HappyPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(ctx).(*FileSession)
	require.NoError(t, s.TouchBin())
	require.NoError(t, s.Persist(ctx))

	got, err := fs.Get(ctx, s.ID())

	require.NoError(t, err)
	assert.Equal(t, s.ID(), got.ID())
	info, err := got.GetInfo(ctx)
	require.NoError(t, err)
	assert.Equal(t, "FileStore", info.Storage["Type"])
}

func TestFileStoreGet_OffsetFromBinSize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(ctx).(*FileSession)
	require.NoError(t, s.TouchBin())
	require.NoError(t, s.Persist(ctx))

	payload := []byte("hello world")
	require.NoError(t, os.WriteFile(s.binPath(), payload, 0600))

	got, err := fs.Get(ctx, s.ID())

	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), got.Offset())
}

func TestFileStoreGet_MissingInfoReturnsErrNotFound(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	_, err := fs.Get(context.Background(), "no-such-id")

	assert.ErrorIs(t, err, tusd.ErrNotFound)
}

func TestFileStoreGet_CorruptInfoReturnsError(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(ctx).(*FileSession)
	require.NoError(t, s.TouchBin())
	require.NoError(t, s.Persist(ctx))

	// Overwrite the .info with garbage JSON.
	require.NoError(t, os.WriteFile(s.infoPath(), []byte("{not valid json"), 0600))

	_, err := fs.Get(ctx, s.ID())

	require.Error(t, err)
	assert.NotErrorIs(t, err, tusd.ErrNotFound)
}

func TestFileStoreGet_MissingBinReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s := fs.New(ctx).(*FileSession)
	// Write .info but do NOT create the .bin file.
	require.NoError(t, s.Persist(ctx))

	_, err := fs.Get(ctx, s.ID())

	assert.ErrorIs(t, err, tusd.ErrNotFound)
}

// FileStore.List

func TestFileStoreList_EmptyDir(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	sessions, err := fs.List(context.Background())

	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestFileStoreList_ReturnsBothSessions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	s1 := fs.New(ctx).(*FileSession)
	require.NoError(t, s1.TouchBin())
	require.NoError(t, s1.Persist(ctx))

	s2 := fs.New(ctx).(*FileSession)
	require.NoError(t, s2.TouchBin())
	require.NoError(t, s2.Persist(ctx))

	sessions, err := fs.List(ctx)

	require.NoError(t, err)
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID())
	}
	assert.ElementsMatch(t, []string{s1.ID(), s2.ID()}, ids)
}

func TestFileStoreList_SkipsSessionWithMissingBin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, nopLog())
	require.NoError(t, fs.Setup())

	good := fs.New(ctx).(*FileSession)
	require.NoError(t, good.TouchBin())
	require.NoError(t, good.Persist(ctx))

	bad := fs.New(ctx).(*FileSession)
	require.NoError(t, bad.TouchBin())
	require.NoError(t, bad.Persist(ctx))
	require.NoError(t, os.Remove(bad.binPath()))

	sessions, err := fs.List(ctx)

	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, good.ID(), sessions[0].ID())
}

// FileStoreFromDriverConf

func TestFileStoreFromDriverConf_NilConfigReturnsNil(t *testing.T) {
	assert.Nil(t, FileStoreFromDriverConf(nil, nopLog()))
}

func TestFileStoreFromDriverConf_RootKey(t *testing.T) {
	root := t.TempDir()
	fs := FileStoreFromDriverConf(map[string]interface{}{"root": root}, nopLog())

	require.NotNil(t, fs)
	assert.Equal(t, root, fs.root)
}

func TestFileStoreFromDriverConf_UploadDirectoryKey(t *testing.T) {
	dir := t.TempDir()
	fs := FileStoreFromDriverConf(map[string]interface{}{"upload_directory": dir}, nopLog())

	require.NotNil(t, fs)
	assert.Equal(t, dir, fs.root)
}

func TestFileStoreFromDriverConf_UploadDirectoryWinsOverRoot(t *testing.T) {
	root := t.TempDir()
	uploadDir := t.TempDir()
	fs := FileStoreFromDriverConf(map[string]interface{}{
		"root":             root,
		"upload_directory": uploadDir,
	}, nopLog())

	require.NotNil(t, fs)
	assert.Equal(t, uploadDir, fs.root)
}

func TestFileStoreFromDriverConf_NeitherKeyReturnsNil(t *testing.T) {
	fs := FileStoreFromDriverConf(map[string]interface{}{"some_other_key": "value"}, nopLog())

	assert.Nil(t, fs)
}

// NewFileStoreFromConfig

func TestNewFileStoreFromConfig_UploadDirUsed(t *testing.T) {
	uploadDir := t.TempDir()
	fs := NewFileStoreFromConfig(uploadDir, map[string]interface{}{"root": "/ignored"}, nopLog())

	require.NotNil(t, fs)
	assert.Equal(t, uploadDir, fs.root)
}

func TestNewFileStoreFromConfig_FallsBackToDriverConf(t *testing.T) {
	root := t.TempDir()
	fs := NewFileStoreFromConfig("", map[string]interface{}{"root": root}, nopLog())

	require.NotNil(t, fs)
	assert.Equal(t, root, fs.root)
}

func TestNewFileStoreFromConfig_BothEmptyReturnsNil(t *testing.T) {
	fs := NewFileStoreFromConfig("", nil, nopLog())

	assert.Nil(t, fs)
}
