package fixture

import (
	"bytes"
	"os"
	"strings"

	"github.com/owncloud/reva/v2/pkg/storage/fs/kiteworks/kwlib"
	"github.com/rs/zerolog"
)

// Manager stages and tears down test content on a real Kiteworks box.
// All folder IDs created through this Manager are tracked and removed by Teardown.
type Manager struct {
	client  *kwlib.APIClient
	created []string
}

// New constructs a Manager talking to endpoint with the given bearer token.
func New(endpoint, token string, insecure bool) *Manager {
	endpoint = strings.TrimRight(endpoint, "/")
	f := kwlib.NewClientFactory(endpoint, "kw-fixture/1.0", insecure)
	l := zerolog.New(os.Stderr).With().Timestamp().Logger()
	return &Manager{client: f.Build("", "", "", token, &l)}
}

// Setup creates a fresh top-level folder named name. Returns its KW folder ID.
func (m *Manager) Setup(name string) (string, error) {
	syncable := true
	id, err := m.client.CreateFolder("0", kwlib.CreateDirRequest{Name: name, SyncAble: &syncable})
	if err != nil {
		return "", err
	}
	m.created = append(m.created, id)
	return id, nil
}

// MkdirAll creates each path component under parentID in order.
// path is slash-separated (e.g. "a/b/c"). Returns the leaf folder ID.
func (m *Manager) MkdirAll(parentID, path string) (string, error) {
	cur := parentID
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if part == "" {
			continue
		}
		id, err := m.client.CreateFolder(cur, kwlib.CreateDirRequest{Name: part})
		if err != nil {
			return "", err
		}
		m.created = append(m.created, id)
		cur = id
	}
	return cur, nil
}

// UploadFile uploads content as name under parentID. Returns the KW file ID.
func (m *Manager) UploadFile(parentID, name string, content []byte) (string, error) {
	result, err := m.client.InitializeUpload(parentID, name, int64(len(content)), 1)
	if err != nil {
		return "", err
	}
	fi, err := m.client.UploadChunk(result.URI, name, bytes.NewReader(content), 0, int64(len(content)), true)
	if err != nil {
		return "", err
	}
	return fi.ID, nil
}

// Track registers an externally-known folder ID for deletion by Teardown.
// Used by the CLI when the ID was obtained in a prior invocation.
func (m *Manager) Track(id string) {
	m.created = append(m.created, id)
}

// Teardown deletes all folders registered during this session in reverse order.
// Errors are printed to stderr but do not stop the loop.
func (m *Manager) Teardown() {
	for i := len(m.created) - 1; i >= 0; i-- {
		_ = m.client.DeleteFolder(m.created[i])
	}
	m.created = m.created[:0]
}
