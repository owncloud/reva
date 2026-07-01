# kw-fixture

```bash
STORAGE_USERS_KITEWORKS_ENDPOINT=<url> \
STORAGE_USERS_KITEWORKS_API_TOKEN=<token> \
  go test -mod=mod ./pkg/storage/fs/kiteworks/ -v --ginkgo.focus "smoke"
```

## Step 1

```bash
export STORAGE_USERS_KITEWORKS_ENDPOINT=<url>
export STORAGE_USERS_KITEWORKS_API_TOKEN=<token>

ROOT_ID=$(go run -mod=mod ./cmd/kw-fixture/ setup ocis-test-step1)
echo "created: $ROOT_ID"
go run -mod=mod ./cmd/kw-fixture/ teardown $ROOT_ID && echo "exit: 0"
```

```
created: <uuid>
exit: 0
```

## Step 2

```bash
ROOT_ID=$(go run -mod=mod ./cmd/kw-fixture/ setup ocis-smoke)
DIR_ID=$(go run -mod=mod ./cmd/kw-fixture/ mkdir $ROOT_ID a/b/c)
FILE_ID=$(go run -mod=mod ./cmd/kw-fixture/ upload $ROOT_ID hello.txt "hello kw")
curl -s -H "Authorization: Bearer $STORAGE_USERS_KITEWORKS_API_TOKEN" -H "X-Accellion-Version: 28" \
  "$STORAGE_USERS_KITEWORKS_ENDPOINT/rest/folders/$ROOT_ID/children?deleted=false" | jq '[.data[]|{name,type}]'
go run -mod=mod ./cmd/kw-fixture/ teardown $ROOT_ID && echo "exit: 0"
```

```
[{"name":"a","type":"d"},{"name":"hello.txt","type":"f"}]
exit: 0
```

## Step 3 — Go smoke tests

```bash
go test -mod=mod ./pkg/storage/fs/kiteworks/ -v --ginkgo.focus "smoke"
```

```
It staged folder appears in ListStorageSpaces PASSED
It uploaded file content round-trips via Download PASSED
It nested mkdir leaf appears in ListFolder PASSED
```

## Step 4 — Behat kw-backed acceptance tests

Requires a running oCIS instance with `storage-users-kiteworks` using the same env vars.

```bash
cd tests/acceptance
vendor/bin/behat --suite=apiKiteworks --tags=@kw-backed
```

```
3 scenarios (3 passed)
```
