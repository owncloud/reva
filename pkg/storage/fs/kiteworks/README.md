# Kiteworks storage driver

`storage.FS` implementation backed by a Kiteworks box: reads in OCISDEV-903, writes/versions/locking in OCISDEV-906, space lifecycle in OCISDEV-907.

Recycle bin, grants, arbitrary metadata, `CreateReference`, and personal space creation (`CreateHome`, `GetHome`, `CreateStorageSpace` with `type=personal` — KW provisions those itself) still return `errtypes.NotSupported`.

---

## Architecture

```
kiteworks/
  kwlib/          thin REST client (copied from kw-webdav-bridge/pkg/kwlib/)
    client.go     APIClientFactory, APIClient, HTTP helpers
    types.go      FileInfo, DirectoryInfo, QuotaInfo, …
  kiteworks.go    storage.FS implementation
  spaces.go       space listing and lifecycle
  versions.go     revisions
  permissions.go  KW permission → CS3 ResourcePermissions mapping
  *_test.go       Ginkgo smoke tests (mock + real-box)
```

**Request flow:**

```
storage.FS method call
  ctxpkg.ContextGetToken(ctx)        → Bearer token from request context
  factory.Build(host, id, addr, tok) → per-request APIClient
  APIClient.GetXxx()                 → GET https://<endpoint>/rest/…
```

The `APIClientFactory` is stateless; a new `APIClient` is constructed for every `storage.FS` call so token rotation is automatic.

---

## Kiteworks REST endpoints

| `storage.FS` method | Endpoint |
|---------------------|----------|
| `ListStorageSpaces`  | `GET /rest/folders/top?deleted={false,true}&with=(permissions)` + `GET /rest/users/me` for `syncdirId` |
| `CreateStorageSpace` | `POST /rest/folders/0/folders` |
| `UpdateStorageSpace` | `PATCH /rest/folders/{id}/actions/recover`, `PUT /rest/folders/{id}` (rename, quota) |
| `DeleteStorageSpace` | `DELETE /rest/folders/{id}`, or `/actions/permanent` when `purge` is set |
| `GetMD`             | `GET /rest/folders/{id}` → fallback `GET /rest/files/{id}` |
| `ListFolder`        | `GET /rest/folders/{id}/children?deleted=false&with=(permissions)` |
| `Download`          | `GET /rest/files/{id}/content` |
| `GetPathByID`       | `GET /rest/folders/{id}` → `.path` → fallback `GET /rest/files/{id}` |
| `GetQuota`          | `GET /rest/quotas` |
| `GetLock`           | no-op, returns nil |
| `ListGrants`        | returns empty slice (mapping not yet implemented) |

Every request carries:
```
Authorization: Bearer <token>
X-Accellion-Version: 28
```

---

## ResourceId mapping

```
StorageId = "kiteworks"
SpaceId   = Kiteworks top-folder ID  (from /rest/folders/top → data[].id)
OpaqueId  = Kiteworks node ID        (folder or file)
```

Each top-level folder becomes one `StorageSpace`. The folder matching the user's `syncdirId` is reported as `personal`, every other one as `project`; if `syncdirId` cannot be resolved, all of them fall back to `project`. Soft-deleted top folders are listed too, marked with the `trashed` opaque entry. Quota writes snap to the five values KW accepts (1/2/5/10/50 GiB, `-1` for unlimited).

---

## Configuration

```toml
[grpc.services.storageprovider.drivers.kiteworks]
endpoint = "https://your-kw-box.example.com"
insecure = false   # skip TLS verification (dev boxes only)
```

Register the driver at startup by importing the loader:

```go
import _ "github.com/owncloud/reva/v2/pkg/storage/fs/loader"
```

The loader already includes `kiteworks` alongside the other drivers.

---

## Testing

**Mock (no network, always runs in CI):**

```bash
go test ./pkg/storage/fs/kiteworks/...
```

The mock spins up an `httptest.Server` with hardcoded fixtures:
- `space-1` — top-level folder "My Docs"
- `file-1` — "hello.txt" (14 bytes, content `"hello kiteworks"`)

**Real box:**

```bash
KITEWORKS=https://your-kw-box.example.com \
KITEWORKS_TOKEN=<bearer-token> \
go test ./pkg/storage/fs/kiteworks/... -v
```

In real-box mode `setupDriver` calls `ListStorageSpaces` + `ListFolder` to discover live IDs before the test suite runs. Tests that need a file (`Download`, `GetPathByID` for files) are skipped automatically when the root space is empty. The mock-only content-exact assertion (`ListFolder` children check) is also skipped.

**Lab box (Kiteworks dev environment):**

```bash
source .env   # exports KW_API_TOKEN
KITEWORKS=https://kwlab-dev-michalklos-27b42b81.acc.guru \
KITEWORKS_TOKEN=$KW_API_TOKEN \
go test ./pkg/storage/fs/kiteworks/... -v
```
