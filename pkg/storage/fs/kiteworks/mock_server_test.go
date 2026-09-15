package kiteworks_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func mockKiteworksHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/folders/top", func(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, `{"id":"user-1","name":"Test User","email":"test@example.com"}`)
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
	versionedFileJSON := `{"id":"versioned-file-1","type":"f","name":"versioned.txt","path":"/My Docs/versioned.txt","size":20,"modified":"2024-01-01T00:00:00+0000","permissions":[{"id":1,"name":"version_view","allowed":true},{"id":2,"name":"version_promote","allowed":true}]}`
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

	return mux
}
