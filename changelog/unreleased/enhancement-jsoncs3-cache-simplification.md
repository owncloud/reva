Enhancement: Simplify the caches of the jsoncs3 share manager

We've changed how the jsoncs3 share manager keeps its documents. Each document
is now a read-only entry in a new document store. A write changes a private
copy and uploads it with an etag condition. A failed upload does not touch the
entry. Reads take no lock, and parallel reads of one document share one
download.

A listing now only filters. A background loop removes expired shares and stale
group entries. The `ShareExpired` event can come up to one minute after the
expiry. `created.json` now gets a cleanup too.

The metadata storage caches the machine token and uses the context of the
caller. A listing of received shares stops after a time budget and returns an
error. A share with a `null` grantee does not panic the decoder any more.

The module now needs Go 1.27.3.

There are three new settings of the driver: `list_timeout` (default 10
seconds), `cleanup_interval` (default 60 seconds) and `group_cache_ttl`
(default 0, which means off). The old setting `ttl` still does nothing.

https://github.com/owncloud/reva/pull/855
