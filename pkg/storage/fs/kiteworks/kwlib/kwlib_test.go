package kwlib_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/storage/fs/kiteworks/kwlib"
	"github.com/rs/zerolog"
)

// --- Time.MarshalJSON ---

func TestTimeMarshalJSON_nil(t *testing.T) {
	var tp *kwlib.Time
	b, err := json.Marshal(tp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(b) != "null" {
		t.Fatalf("want null, got %s", b)
	}
}

func TestTimeMarshalJSON_roundtrip(t *testing.T) {
	original := kwlib.Time(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))
	b, err := json.Marshal(&original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got kwlib.Time
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if time.Time(original) != time.Time(got) {
		t.Fatalf("want %v, got %v", time.Time(original), time.Time(got))
	}
}

// --- decode error propagation ---

func badJSONServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
}

func newClient(t *testing.T, srv *httptest.Server) *kwlib.APIClient {
	t.Helper()
	f := kwlib.NewClientFactory(srv.URL, "", false)
	nop := zerolog.Nop()
	return f.Build("", "", "", "tok", &nop)
}

func TestDecodeError_returnsNil(t *testing.T) {
	srv := badJSONServer()
	defer srv.Close()
	c := newClient(t, srv)

	cases := []struct {
		name string
		call func() (any, error)
	}{
		{"GetTopFolders", func() (any, error) { return c.GetTopFolders(false) }},
		{"GetFolderByID", func() (any, error) { return c.GetFolderByID("x") }},
		{"GetFileByID", func() (any, error) { return c.GetFileByID("x") }},
		{"ListFolderContents", func() (any, error) { v, e := c.ListFolderContents("x"); return v, e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// --- SendRequest: 4xx/5xx is error ---

func serverWith(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestSendRequest_404_isError(t *testing.T) {
	srv := serverWith(http.StatusNotFound, `{"error":"nope"}`)
	defer srv.Close()
	c := newClient(t, srv)
	req, _ := c.NewGetRequest("/rest/folders/top")
	_, err := c.SendRequest(req)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusNotFound {
		t.Fatalf("expected ClientError(404), got %v", err)
	}
}

func TestSendRequest_500_isError(t *testing.T) {
	srv := serverWith(http.StatusInternalServerError, `oops`)
	defer srv.Close()
	c := newClient(t, srv)
	req, _ := c.NewGetRequest("/rest/folders/top")
	_, err := c.SendRequest(req)
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

// --- Write methods ---

func TestDeleteFolder_success(t *testing.T) {
	srv := serverWith(http.StatusNoContent, "")
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.DeleteFolder("folder-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteFolder_error(t *testing.T) {
	srv := serverWith(http.StatusForbidden, `{"error":"forbidden"}`)
	defer srv.Close()
	c := newClient(t, srv)
	err := c.DeleteFolder("folder-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusForbidden {
		t.Fatalf("expected ClientError(403), got %v", err)
	}
}

func TestRecoverFolder_sendsPatchToRecoverAction(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.RecoverFolder("folder-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/rest/folders/folder-1/actions/recover" {
		t.Fatalf("expected PATCH /rest/folders/folder-1/actions/recover, got %s %s", gotMethod, gotPath)
	}
}

func TestRecoverFolder_error(t *testing.T) {
	srv := serverWith(http.StatusForbidden, `{"errors":[{"code":"ERR_ENTITY_NOT_DELETED"}]}`)
	defer srv.Close()
	c := newClient(t, srv)
	err := c.RecoverFolder("folder-1")
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusForbidden {
		t.Fatalf("expected ClientError(403), got %v", err)
	}
}

func TestDeleteFile_success(t *testing.T) {
	srv := serverWith(http.StatusNoContent, "")
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.DeleteFile("file-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMoveFolder_success(t *testing.T) {
	srv := serverWith(http.StatusOK, "")
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.MoveFolder("src-1", "dst-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateFolder_parsesLocationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Accellion-Location", "/rest/folders/new-folder-123")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c := newClient(t, srv)
	id, err := c.CreateFolder("parent-1", kwlib.CreateDirRequest{Name: "NewFolder"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "new-folder-123" {
		t.Fatalf("want new-folder-123, got %q", id)
	}
}

// mirrors the unexported uploadChunkSize in the kwlib package
const testChunkSize = 8 << 20

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

type uploadRecorder struct {
	totalChunks  int
	filename     string
	chunkIndexes []string
	chunkSizes   []int64
	terminated   []string
}

// versionUploadServer serves an initiateUpload + chunk session for file-1,
// failing every chunk POST with chunkStatus when it is >= 400.
func versionUploadServer(t *testing.T, rec *uploadRecorder, chunkStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/files/file-1/actions/initiateUpload", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			FileName    string `json:"filename"`
			TotalChunks int    `json:"totalChunks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode initiate payload: %v", err)
		}
		rec.filename = payload.FileName
		rec.totalChunks = payload.TotalChunks
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"uri":"uploads/sess-42"}`))
	})

	mux.HandleFunc("/uploads/sess-42", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			t.Errorf("multipart reader: %v", err)
			return
		}
		var index string
		var size int64
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			switch part.FormName() {
			case "content":
				size, _ = io.Copy(io.Discard, part)
			case "index":
				b, _ := io.ReadAll(part)
				index = string(b)
			default:
				_, _ = io.Copy(io.Discard, part)
			}
		}
		rec.chunkIndexes = append(rec.chunkIndexes, index)
		rec.chunkSizes = append(rec.chunkSizes, size)

		if chunkStatus >= 400 {
			w.WriteHeader(chunkStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"file-1","type":"f","name":"doc.txt"}`))
	})

	mux.HandleFunc("/rest/uploads/42", func(w http.ResponseWriter, r *http.Request) {
		rec.terminated = append(rec.terminated, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	return httptest.NewServer(mux)
}

func TestUploadFileVersion_singleChunk(t *testing.T) {
	rec := &uploadRecorder{}
	srv := versionUploadServer(t, rec, 0)
	defer srv.Close()
	c := newClient(t, srv)

	if err := c.UploadFileVersion(context.Background(), "file-1", "doc.txt", strings.NewReader("content"), 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.filename != "doc.txt" {
		t.Fatalf("want filename doc.txt, got %q", rec.filename)
	}
	if rec.totalChunks != 1 {
		t.Fatalf("want totalChunks 1, got %d", rec.totalChunks)
	}
	if len(rec.chunkSizes) != 1 || rec.chunkSizes[0] != 7 {
		t.Fatalf("want one 7-byte chunk, got %v", rec.chunkSizes)
	}
	if rec.chunkIndexes[0] != "1" {
		t.Fatalf("chunk index is 1-based, got %q", rec.chunkIndexes[0])
	}
	if len(rec.terminated) != 0 {
		t.Fatalf("session should not be terminated on success, got %v", rec.terminated)
	}
}

func TestUploadFileVersion_zeroLengthSendsOneChunk(t *testing.T) {
	rec := &uploadRecorder{}
	srv := versionUploadServer(t, rec, 0)
	defer srv.Close()
	c := newClient(t, srv)

	if err := c.UploadFileVersion(context.Background(), "file-1", "empty.txt", strings.NewReader(""), 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.totalChunks != 1 {
		t.Fatalf("want totalChunks 1 for an empty file, got %d", rec.totalChunks)
	}
	if len(rec.chunkSizes) != 1 || rec.chunkSizes[0] != 0 {
		t.Fatalf("want one 0-byte chunk, got %v", rec.chunkSizes)
	}
}

func TestUploadFileVersion_splitsIntoChunks(t *testing.T) {
	rec := &uploadRecorder{}
	srv := versionUploadServer(t, rec, 0)
	defer srv.Close()
	c := newClient(t, srv)

	length := int64(testChunkSize + 100)
	body := io.LimitReader(zeroReader{}, length)
	if err := c.UploadFileVersion(context.Background(), "file-1", "big.bin", body, length); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.totalChunks != 2 {
		t.Fatalf("want totalChunks 2, got %d", rec.totalChunks)
	}
	want := []int64{testChunkSize, 100}
	if len(rec.chunkSizes) != 2 || rec.chunkSizes[0] != want[0] || rec.chunkSizes[1] != want[1] {
		t.Fatalf("want chunk sizes %v, got %v", want, rec.chunkSizes)
	}
	if rec.chunkIndexes[0] != "1" || rec.chunkIndexes[1] != "2" {
		t.Fatalf("want indexes [1 2], got %v", rec.chunkIndexes)
	}
}

func TestUploadFileVersion_chunkErrorTerminatesSession(t *testing.T) {
	rec := &uploadRecorder{}
	srv := versionUploadServer(t, rec, http.StatusForbidden)
	defer srv.Close()
	c := newClient(t, srv)

	err := c.UploadFileVersion(context.Background(), "file-1", "doc.txt", strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusForbidden {
		t.Fatalf("expected ClientError(403), got %v", err)
	}
	if len(rec.terminated) != 1 || rec.terminated[0] != http.MethodDelete {
		t.Fatalf("want one DELETE to discard the session, got %v", rec.terminated)
	}
}

func TestUploadFileVersion_initiateError(t *testing.T) {
	srv := serverWith(http.StatusForbidden, `{"error":"forbidden"}`)
	defer srv.Close()
	c := newClient(t, srv)

	err := c.UploadFileVersion(context.Background(), "file-1", "doc.txt", strings.NewReader("x"), 1)
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusForbidden {
		t.Fatalf("expected ClientError(403), got %v", err)
	}
}

func TestGetFileVersions_success(t *testing.T) {
	body := `{"data":[{"id":"v1","versionNumber":1,"size":0},{"id":"v2","versionNumber":2,"size":100}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	versions, err := c.GetFileVersions("file-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
	if versions[0].ID != "v1" || versions[0].VersionNumber != 1 || versions[0].Size != 0 {
		t.Fatalf("unexpected first version: %+v", versions[0])
	}
	if versions[1].ID != "v2" || versions[1].Size != 100 {
		t.Fatalf("unexpected second version: %+v", versions[1])
	}
}

func TestGetFileVersions_decodeError(t *testing.T) {
	srv := badJSONServer()
	defer srv.Close()
	c := newClient(t, srv)
	if _, err := c.GetFileVersions("file-1"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDeleteFileVersion_success(t *testing.T) {
	srv := serverWith(http.StatusNoContent, "")
	defer srv.Close()
	c := newClient(t, srv)
	if err := c.DeleteFileVersion("file-1", "v1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteFileVersion_422(t *testing.T) {
	srv := serverWith(http.StatusUnprocessableEntity, `{"error":"last version"}`)
	defer srv.Close()
	c := newClient(t, srv)
	err := c.DeleteFileVersion("file-1", "v1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected ClientError(422), got %v", err)
	}
}

func TestRenameFolder_success(t *testing.T) {
	srv := serverWith(http.StatusOK, "")
	defer srv.Close()
	c := newClient(t, srv)
	parentID := "p1"
	fi := &kwlib.FileInfo{ID: "f1", Type: kwlib.DirectoryType, Name: "OldName", ParentID: &parentID}
	ok, err := c.RenameFolder(fi, "NewName")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
}

func TestRenameFile_success(t *testing.T) {
	srv := serverWith(http.StatusOK, "")
	defer srv.Close()
	c := newClient(t, srv)
	fi := &kwlib.FileInfo{ID: "f1", Type: kwlib.FileType, Name: "old.txt"}
	ok, err := c.RenameFile(fi, "new.txt", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
}

func TestGetFolderQuota_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/folders/folder-1/quota" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"storage_quota":1000,"storage_used":250,"storage_available":750}`))
	}))
	defer srv.Close()
	c := newClient(t, srv)
	q, err := c.GetFolderQuota("folder-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.StorageQuota != 1000 || q.StorageUsed != 250 || q.StorageAvailable != 750 {
		t.Fatalf("unexpected quota: %+v", q)
	}
}

func TestGetFolderQuota_forbidden(t *testing.T) {
	srv := serverWith(http.StatusForbidden, `{"error":"forbidden"}`)
	defer srv.Close()
	c := newClient(t, srv)
	_, err := c.GetFolderQuota("folder-1")
	var ce *kwlib.ClientError
	if !errors.As(err, &ce) || ce.StatusCode != http.StatusForbidden {
		t.Fatalf("expected ClientError(403), got %v", err)
	}
}

func TestMoveFile_success(t *testing.T) {
	srv := serverWith(http.StatusOK, "")
	defer srv.Close()
	c := newClient(t, srv)
	src := &kwlib.FileInfo{ID: "src-1", Type: kwlib.FileType, Name: "file.txt"}
	dst := &kwlib.FileInfo{ID: "dst-folder-1"}
	ok, err := c.Move(src, dst, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
}
