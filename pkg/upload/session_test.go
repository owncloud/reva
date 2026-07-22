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
	"crypto/sha1" //nolint:gosec
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/utils"
)

func newTestSession(t *testing.T) (*FileSession, *FileStore) {
	t.Helper()
	log := zerolog.Nop()
	fs := NewFileStore(t.TempDir(), TokenOptions{}, &log)
	sess := fs.New(context.Background()).(*FileSession)
	return sess, fs
}

// SetMetadata / SetStorageValue / SetSize / SetSizeIsDeferred

func TestSetMetadata(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetMetadata("providerID", "storage-1")
	assert.Equal(t, "storage-1", sess.ProviderID())
}

func TestSetStorageValue(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetStorageValue("SpaceRoot", "space-abc")
	assert.Equal(t, "space-abc", sess.SpaceID())
}

func TestSetSize(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetSize(1234)
	assert.Equal(t, int64(1234), sess.Size())
}

func TestSetSizeIsDeferred(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetSizeIsDeferred(true)
	assert.True(t, sess.info.SizeIsDeferred)
	sess.SetSizeIsDeferred(false)
	assert.False(t, sess.info.SizeIsDeferred)
}

// SetExecutant / Executant

func TestSetExecutant_Executant(t *testing.T) {
	sess, _ := newTestSession(t)
	u := &userpb.User{
		Id: &userpb.UserId{
			Idp:      "idp.example.com",
			OpaqueId: "user-42",
			Type:     userpb.UserType_USER_TYPE_PRIMARY,
		},
		Username:    "alice",
		DisplayName: "Alice",
	}
	sess.SetExecutant(u)
	got := sess.Executant()
	assert.Equal(t, "idp.example.com", got.Idp)
	assert.Equal(t, "user-42", got.OpaqueId)
	assert.Equal(t, userpb.UserType_USER_TYPE_PRIMARY, got.Type)
}

// NodeExists

func TestNodeExists(t *testing.T) {
	sess, _ := newTestSession(t)

	assert.False(t, sess.NodeExists(), "absent key should return false")

	sess.SetStorageValue("NodeExists", "true")
	assert.True(t, sess.NodeExists())

	sess.SetStorageValue("NodeExists", "false")
	assert.False(t, sess.NodeExists())

	sess.SetStorageValue("NodeExists", "yes")
	assert.False(t, sess.NodeExists(), "non-'true' value should return false")
}

// Checksums / SetChecksums

func TestChecksums_SetChecksums(t *testing.T) {
	sess, _ := newTestSession(t)
	sha1bytes := []byte{0x01, 0x02, 0x03, 0x04}
	md5bytes := []byte{0x05, 0x06, 0x07, 0x08}
	adlerBytes := []byte{0x09, 0x0a, 0x0b, 0x0c}

	sess.SetChecksums(sha1bytes, md5bytes, adlerBytes)
	got := sess.Checksums()
	assert.Equal(t, sha1bytes, got.SHA1)
	assert.Equal(t, md5bytes, got.MD5)
	assert.Equal(t, adlerBytes, got.Adler32)
}

// ScanData / SetScanData

func TestScanData_SetScanData(t *testing.T) {
	sess, _ := newTestSession(t)

	result, date := sess.ScanData()
	assert.Empty(t, result, "fresh session should have no scan result")
	assert.True(t, date.IsZero(), "fresh session should have zero scan date")

	now := time.Now().Truncate(time.Second)
	sess.SetScanData("clean", now)
	result, date = sess.ScanData()
	assert.Equal(t, "clean", result)
	assert.WithinDuration(t, now, date, time.Second)
}

// Expires

func TestExpires(t *testing.T) {
	sess, _ := newTestSession(t)
	assert.True(t, sess.Expires().IsZero(), "absent expires should be zero time")

	want := time.Now().Add(time.Hour).Truncate(time.Second)
	sess.SetMetadata("expires", utils.TimeToOCMtime(want))
	got := sess.Expires()
	assert.WithinDuration(t, want, got, time.Second)
}

// IsProcessing

func TestIsProcessing(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetSize(100)

	assert.False(t, sess.IsProcessing(), "size != offset should not be processing")

	sess.info.Offset = 100
	assert.True(t, sess.IsProcessing(), "size == offset with no scan result should be processing")

	sess.SetScanData("clean", time.Now())
	assert.False(t, sess.IsProcessing(), "scan result set means processing finished")
}

// Reference

func TestReference(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetMetadata("providerID", "prov-1")
	sess.SetStorageValue("SpaceRoot", "space-2")
	sess.SetStorageValue("NodeId", "node-3")

	ref := sess.Reference()
	require.NotNil(t, ref.ResourceId)
	assert.Equal(t, "prov-1", ref.ResourceId.StorageId)
	assert.Equal(t, "space-2", ref.ResourceId.SpaceId)
	assert.Equal(t, "node-3", ref.ResourceId.OpaqueId)
}

// Metadata

func TestMetadata(t *testing.T) {
	sess, _ := newTestSession(t)
	sess.SetMetadata("providerID", "p1")
	sess.SetMetadata("mtime", "12345.0")
	sess.SetStorageValue("NodeExists", "true")
	sess.SetMetadata("versionsPath", "/v/path")

	m := sess.Metadata()
	assert.Equal(t, "p1", m["providerID"])
	assert.Equal(t, "12345.0", m["mtime"])
	assert.Equal(t, "true", m["nodeExists"])
	assert.Equal(t, "/v/path", m["versionsPath"])
	assert.Equal(t, sess.ID(), m["sessionID"])
}

// Persist + Get round-trip

func TestPersist_GetRoundtrip(t *testing.T) {
	log := zerolog.Nop()
	root := t.TempDir()
	fs := NewFileStore(root, TokenOptions{}, &log)
	require.NoError(t, fs.Setup())

	sess := fs.New(context.Background()).(*FileSession)
	sess.SetSize(512)
	sess.SetMetadata("providerID", "prov-rt")
	sess.SetStorageValue("NodeId", "nd-rt")
	sess.SetStorageValue("NodeExists", "true")

	require.NoError(t, sess.TouchBin())
	require.NoError(t, sess.Persist(context.Background()))

	loaded, err := fs.Get(context.Background(), sess.ID())
	require.NoError(t, err)
	ls := loaded.(*FileSession)

	assert.Equal(t, int64(512), ls.Size())
	assert.Equal(t, "prov-rt", ls.ProviderID())
	assert.Equal(t, "nd-rt", ls.NodeID())
	assert.True(t, ls.NodeExists())
}

func TestPersist_CreatesIntermediateDirs(t *testing.T) {
	log := zerolog.Nop()
	root := filepath.Join(t.TempDir(), "sub", "nested")
	fs := NewFileStore(root, TokenOptions{}, &log)

	sess := fs.New(context.Background()).(*FileSession)
	sess.SetSize(1)

	err := sess.Persist(context.Background())
	assert.NoError(t, err)
	_, statErr := os.Stat(sess.infoPath())
	assert.NoError(t, statErr)
}

// WriteChunk

func TestWriteChunk(t *testing.T) {
	sess, _ := newTestSession(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(sess.binPath()), 0700))
	require.NoError(t, sess.TouchBin())

	n, err := sess.WriteChunk(context.Background(), 0, strings.NewReader("hello"))
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
	assert.Equal(t, int64(5), sess.Offset())

	n2, err := sess.WriteChunk(context.Background(), 5, strings.NewReader(" world"))
	require.NoError(t, err)
	assert.Equal(t, int64(6), n2)
	assert.Equal(t, int64(11), sess.Offset())

	data, err := os.ReadFile(sess.binPath())
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
}

func TestWriteChunk_NoBinFile(t *testing.T) {
	sess, _ := newTestSession(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(sess.binPath()), 0700))

	_, err := sess.WriteChunk(context.Background(), 0, strings.NewReader("data"))
	assert.Error(t, err)
}

// Cleanup

func setupCleanupSession(t *testing.T) *FileSession {
	t.Helper()
	sess, _ := newTestSession(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(sess.binPath()), 0700))
	require.NoError(t, sess.TouchBin())
	require.NoError(t, sess.Persist(context.Background()))
	return sess
}

func TestCleanup_BinOnly(t *testing.T) {
	sess := setupCleanupSession(t)
	sess.Cleanup(true, false)
	_, err := os.Stat(sess.binPath())
	assert.True(t, os.IsNotExist(err), "bin should be removed")
	_, err = os.Stat(sess.infoPath())
	assert.NoError(t, err, "info should survive")
}

func TestCleanup_InfoOnly(t *testing.T) {
	sess := setupCleanupSession(t)
	sess.Cleanup(false, true)
	_, err := os.Stat(sess.binPath())
	assert.NoError(t, err, "bin should survive")
	_, err = os.Stat(sess.infoPath())
	assert.True(t, os.IsNotExist(err), "info should be removed")
}

func TestCleanup_Both(t *testing.T) {
	sess := setupCleanupSession(t)
	sess.Cleanup(true, true)
	_, err := os.Stat(sess.binPath())
	assert.True(t, os.IsNotExist(err), "bin should be removed")
	_, err = os.Stat(sess.infoPath())
	assert.True(t, os.IsNotExist(err), "info should be removed")
}

func TestCleanup_Neither(t *testing.T) {
	sess := setupCleanupSession(t)
	sess.Cleanup(false, false)
	_, err := os.Stat(sess.binPath())
	assert.NoError(t, err, "bin should survive")
	_, err = os.Stat(sess.infoPath())
	assert.NoError(t, err, "info should survive")
}

func TestCleanup_MissingFiles_NoError(t *testing.T) {
	sess, _ := newTestSession(t)
	assert.NotPanics(t, func() {
		sess.Cleanup(true, true)
	})
}

// calculateChecksums

func TestCalculateChecksums(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0600))

	sha1h, md5h, adler32h, err := calculateChecksums(context.Background(), path)
	require.NoError(t, err)

	assert.Equal(t, "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d", hex.EncodeToString(sha1h.Sum(nil)))
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", hex.EncodeToString(md5h.Sum(nil)))
	assert.Equal(t, "062c0215", hex.EncodeToString(adler32h.Sum(nil)))
}

func TestCalculateChecksums_MissingFile(t *testing.T) {
	_, _, _, err := calculateChecksums(context.Background(), "/nonexistent/path/file.bin")
	assert.Error(t, err)
}

// checkHash

func TestCheckHash_Correct(t *testing.T) {
	h := sha1.New() //nolint:gosec
	h.Write([]byte("hello"))
	err := checkHash("aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d", h)
	assert.NoError(t, err)
}

func TestCheckHash_Mismatch(t *testing.T) {
	h := sha1.New() //nolint:gosec
	h.Write([]byte("hello"))
	err := checkHash("0000000000000000000000000000000000000000", h)
	require.Error(t, err)
	assert.ErrorAs(t, err, new(errtypes.ChecksumMismatch))
}

// joinURLParts

func TestJoinURLParts(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"http://host/", "path"}, "http://host/path"},
		{[]string{"http://host", "path"}, "http://host/path"},
		{[]string{"http://host"}, "http://host"},
		{[]string{"http://host", "a", "b"}, "http://host/a/b"},
		{[]string{"http://host/", "a/", "b"}, "http://host/a/b"},
	}
	for _, tc := range tests {
		got := joinURLParts(tc.parts...)
		assert.Equal(t, tc.want, got, "joinURLParts(%v)", tc.parts)
	}
}

// Context

func TestContext(t *testing.T) {
	log := zerolog.Nop()
	fs := NewFileStore(t.TempDir(), TokenOptions{}, &log)
	sess := fs.New(context.Background()).(*FileSession)

	u := &userpb.User{
		Id: &userpb.UserId{
			Idp:      "idp.test",
			OpaqueId: "ctx-user",
			Type:     userpb.UserType_USER_TYPE_PRIMARY,
		},
		Username: "ctxuser",
	}
	sess.SetExecutant(u)
	sess.SetMetadata("lockid", "lock-xyz")
	sess.SetMetadata("initiatorid", "initiator-abc")

	ctx := sess.Context(context.Background())

	gotUser, ok := ctxpkg.ContextGetUser(ctx)
	require.True(t, ok)
	assert.Equal(t, "ctx-user", gotUser.GetId().GetOpaqueId())
	assert.Equal(t, "idp.test", gotUser.GetId().GetIdp())

	lockID, ok := ctxpkg.ContextGetLockID(ctx)
	require.True(t, ok)
	assert.Equal(t, "lock-xyz", lockID)

	initiator, ok := ctxpkg.ContextGetInitiator(ctx)
	require.True(t, ok)
	assert.Equal(t, "initiator-abc", initiator)
}

// URL

func TestURL(t *testing.T) {
	log := zerolog.Nop()
	opts := TokenOptions{
		DownloadEndpoint:     "http://download.example.com",
		DataGatewayEndpoint:  "http://gateway.example.com",
		TransferSharedSecret: "s3cr3t",
		TransferExpires:      3600,
	}
	fs := NewFileStore(t.TempDir(), opts, &log)
	sess := fs.New(context.Background()).(*FileSession)

	url, err := sess.URL(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, url)
	assert.True(t, strings.HasPrefix(url, "http://gateway.example.com"), "URL should start with DataGatewayEndpoint")
}

func TestURL_NonEmpty(t *testing.T) {
	log := zerolog.Nop()
	opts := TokenOptions{
		DataGatewayEndpoint:  "http://gw.example.com",
		TransferSharedSecret: "secret",
		TransferExpires:      60,
	}
	fs := NewFileStore(t.TempDir(), opts, &log)
	sess := fs.New(context.Background()).(*FileSession)

	url1, err := sess.URL(context.Background())
	require.NoError(t, err)
	url2, err := sess.URL(context.Background())
	require.NoError(t, err)

	assert.NotEmpty(t, url1)
	assert.NotEmpty(t, url2)
}
