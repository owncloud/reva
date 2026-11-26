Bugfix: Make jsoncs3 share cache locks cancellable

The jsoncs3 share manager guarded its per-user, per-group and per-space
caches with plain mutexes and held them while downloading the cache file
from the metadata storage. Under contention, requests queued behind a slow
holder kept waiting, and kept their memory, long after the client had
given up, because waiting for the lock ignored the request context.

Acquiring these locks now respects the request context: a waiter whose
context is canceled or times out returns immediately with the context
error instead of waiting for the lock and then still downloading the
cache. Under load the service now degrades in latency instead of piling
up abandoned requests.

https://github.com/owncloud/reva/pull/866
https://github.com/owncloud/reva/pull/838
