package kiteworks_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// mockState exposes what the driver sent to the mock server, for specs that need
// to assert on the request body rather than on the driver's return value, and lets
// them fail individual endpoints to exercise the driver's best-effort paths.
type mockState struct {
	quotaBody      atomic.Value // string: last body PUT to /rest/folders/space-quota-1
	failMe         atomic.Bool
	failDeletedTop atomic.Bool
	permDeleted    atomic.Bool // set when /actions/permanent was called
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func mockKiteworksHandler() (http.Handler, *mockState) {
	mux := http.NewServeMux()
	state := &mockState{}

	mux.HandleFunc("/rest/folders/top", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("deleted") == "true" {
			if state.failDeletedTop.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			writeJSON(w, `{"data":[{"id":"deleted-space-1","type":"d","name":"Deleted Space","path":"/Deleted Space","modified":"2024-01-01T00:00:00+0000","deleted":true}]}`)
			return
		}
		writeJSON(w, `{"data":[{"id":"space-1","type":"d","name":"My Docs","path":"/My Docs","modified":"2024-01-01T00:00:00+0000"}]}`)
	})
	mux.HandleFunc("/rest/folders/space-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"id":"space-1","type":"d","name":"My Docs","path":"/My Docs","modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/folders/space-1/children", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"data":[{"id":"file-1","type":"f","name":"hello.txt","path":"/My Docs/hello.txt","size":14,"modified":"2024-01-01T00:00:00+0000"}]}`)
	})
	mux.HandleFunc("/rest/files/file-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"id":"file-1","type":"f","name":"hello.txt","path":"/My Docs/hello.txt","size":14,"modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/files/file-1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello kiteworks"))
	})
	mux.HandleFunc("/rest/folders/space-1/actions/initiateUpload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, `{"id":1,"uri":"uploads/touch-mock-1","totalSize":0}`)
	})
	mux.HandleFunc("/uploads/touch-mock-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, `{"id":"touched-1","type":"f","name":"newfile.txt","path":"/My Docs/newfile.txt","size":0,"modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/folders/space-1/folders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accellion-Location", "/rest/folders/new-dir-1")
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/rest/users/me", func(w http.ResponseWriter, r *http.Request) {
		if state.failMe.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, `{"id":"user-1","name":"Test User","email":"test@example.com","syncdirId":"space-1"}`)
	})
	mux.HandleFunc("/rest/folders/space-1/quota", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"storage_quota":1073741824,"storage_used":14,"storage_available":1073741810}`)
	})
	// no quota applied to this folder
	mux.HandleFunc("/rest/folders/space-2/quota", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"storage_quota":0,"storage_used":99,"storage_available":0}`)
	})
	// viewers lack file_add, so KW rejects the quota lookup
	mux.HandleFunc("/rest/folders/no-quota-perm-1/quota", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	// Move test nodes
	mux.HandleFunc("/rest/folders/src-folder-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			writeJSON(w, `{"id":"src-folder-1","type":"d","name":"SrcFolder","path":"/My Docs/SrcFolder","parentId":"space-1","modified":"2024-01-01T00:00:00+0000"}`)
		}
	})
	mux.HandleFunc("/rest/folders/src-folder-1/actions/move", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/files/src-file-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			writeJSON(w, `{"id":"src-file-1","type":"f","name":"src.txt","path":"/My Docs/src.txt","parentId":"space-1","size":10,"modified":"2024-01-01T00:00:00+0000"}`)
		}
	})
	mux.HandleFunc("/rest/files/actions/move", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Delete test nodes
	mux.HandleFunc("/rest/folders/folder-del-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, `{"id":"folder-del-1","type":"d","name":"ToDelete","path":"/My Docs/ToDelete","parentId":"space-1","modified":"2024-01-01T00:00:00+0000"}`)
		}
	})
	mux.HandleFunc("/rest/folders/file-only-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/rest/files/file-only-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, `{"id":"file-only-1","type":"f","name":"fileonly.txt","path":"/My Docs/fileonly.txt","size":5,"modified":"2024-01-01T00:00:00+0000"}`)
		}
	})
	mux.HandleFunc("/rest/files/rollback-gone-1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	// CommitUpload / version endpoints
	mux.HandleFunc("/rest/files/ver-file-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"id":"ver-file-1","type":"f","name":"versionable.txt","path":"/My Docs/versionable.txt","size":17,"modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/files/ver-file-1/actions/initiateUpload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, `{"id":7,"uri":"uploads/version-mock-1"}`)
	})
	mux.HandleFunc("/uploads/version-mock-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, `{"id":"ver-file-1","type":"f","name":"versionable.txt","path":"/My Docs/versionable.txt","size":17,"modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/uploads/7", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/rest/files/ver-file-1/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"data":[{"id":"ver-1","versionNumber":1,"size":0}]}`)
	})
	mux.HandleFunc("/rest/files/ver-file-1/versions/ver-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	var lockState int32 // 0 = unlocked, 1 = locked; accessed via atomic ops
	mux.HandleFunc("/rest/files/lock-file-1", func(w http.ResponseWriter, r *http.Request) {
		lockedVal := "false"
		if atomic.LoadInt32(&lockState) == 1 {
			lockedVal = "true"
		}
		writeJSON(w, `{"id":"lock-file-1","type":"f","name":"lockable.txt","path":"/My Docs/lockable.txt","size":10,"modified":"2024-01-01T00:00:00+0000","locked":`+lockedVal+`}`)
	})
	mux.HandleFunc("/rest/files/lock-file-1/actions/lock", func(w http.ResponseWriter, r *http.Request) {
		if !atomic.CompareAndSwapInt32(&lockState, 0, 1) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/files/lock-file-1/actions/unlock", func(w http.ResponseWriter, r *http.Request) {
		if !atomic.CompareAndSwapInt32(&lockState, 1, 0) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/files/ext-locked-file-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"id":"ext-locked-file-1","type":"f","name":"ext-locked.txt","path":"/My Docs/ext-locked.txt","size":5,"modified":"2024-01-01T00:00:00+0000","locked":true}`)
	})

	// Versioning test nodes
	versionedFileJSON := `{"id":"versioned-file-1","type":"f","name":"versioned.txt","path":"/My Docs/versioned.txt","size":20,"modified":"2024-01-01T00:00:00+0000","permissions":[{"id":1,"name":"version_view","allowed":true},{"id":2,"name":"version_promote","allowed":true},{"id":3,"name":"download","allowed":true}]}`
	mux.HandleFunc("/rest/files/versioned-file-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, versionedFileJSON)
	})
	mux.HandleFunc("/rest/files/versioned-file-1/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"data":[{"id":"rev-1","versionNumber":1,"size":10,"created":"2024-01-01T00:00:00+0000"},{"id":"rev-2","versionNumber":2,"size":20,"created":"2024-02-01T00:00:00+0000"}]}`)
	})
	mux.HandleFunc("/rest/files/versioned-file-1/versions/rev-2/content", func(w http.ResponseWriter, r *http.Request) {
		body := "version 2 content"
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/rest/files/versioned-file-1/versions/rev-2/actions/promote", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Space lifecycle (CreateStorageSpace / UpdateStorageSpace / DeleteStorageSpace)
	mux.HandleFunc("/rest/folders/0/folders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Accellion-Location", "/rest/folders/new-space-1")
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/rest/folders/new-space-1/actions/permanent", func(w http.ResponseWriter, _ *http.Request) {
		state.permDeleted.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/rest/folders/new-space-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, `{"id":"new-space-1","type":"d","name":"New Space","path":"/New Space","modified":"2024-01-01T00:00:00+0000"}`)
	})
	renamed := &atomic.Bool{}
	mux.HandleFunc("/rest/folders/space-rename-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			renamed.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		name := "Old Space"
		if renamed.Load() {
			name = "Renamed Space"
		}
		writeJSON(w, `{"id":"space-rename-1","type":"d","name":"`+name+`","path":"/`+name+`","modified":"2024-01-01T00:00:00+0000"}`)
	})
	mux.HandleFunc("/rest/folders/space-quota-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			state.quotaBody.Store(string(b))
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, `{"id":"space-quota-1","type":"d","name":"Quota Space","path":"/Quota Space","modified":"2024-01-01T00:00:00+0000"}`)
	})

	// A deleted space that can be recovered. KW only grants folder_recover while
	// the folder is deleted, and answers 403 when it is not.
	var restoreState int32 = 1 // 1 = deleted, 0 = active; accessed via atomic ops
	mux.HandleFunc("/rest/folders/space-restore-1", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(&restoreState) == 1 {
			writeJSON(w, `{"id":"space-restore-1","type":"d","name":"Restorable Space","path":"/Restorable Space","modified":"2024-01-01T00:00:00+0000","deleted":true,"permissions":[{"id":24,"name":"folder_recover","allowed":true}]}`)
			return
		}
		writeJSON(w, `{"id":"space-restore-1","type":"d","name":"Restorable Space","path":"/Restorable Space","modified":"2024-01-01T00:00:00+0000","deleted":false}`)
	})
	mux.HandleFunc("/rest/folders/space-restore-1/actions/recover", func(w http.ResponseWriter, _ *http.Request) {
		if !atomic.CompareAndSwapInt32(&restoreState, 1, 0) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/folders/space-no-recover-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"id":"space-no-recover-1","type":"d","name":"Locked Down Space","path":"/Locked Down Space","modified":"2024-01-01T00:00:00+0000","deleted":true,"permissions":[{"id":16,"name":"properties_view","allowed":true}]}`)
	})

	serverError := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}
	mux.HandleFunc("/rest/folders/error-500", serverError)
	mux.HandleFunc("/rest/files/error-500", serverError)
	mux.HandleFunc("/rest/folders/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/rest/folders/")
		id = strings.Split(id, "/")[0]
		http.Error(w, `{"error":"not found","id":"`+id+`"}`, http.StatusNotFound)
	})

	return mux, state
}
