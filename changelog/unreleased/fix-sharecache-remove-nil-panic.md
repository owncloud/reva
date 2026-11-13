Bugfix: Fix a nil pointer panic in the jsoncs3 share cache Remove

`sharecache.Cache.Remove` read the share IDs of a space with no nil check. When
the user had no entry for the space of the share, `Remove` panicked. A share ID
without a delimiter caused the same panic. The panic occurred in an errgroup
goroutine, so it stopped the whole process. All removal paths were affected,
for example `Unshare` and the cleanup of orphaned shares.

`Remove` now skips the delete when the space entry is absent. It still persists
the cache. When the in-memory cache is stale, the etag conflict causes a resync
and a retry. Thus the share is removed correctly when another node added it.

We also fixed two related problems in the jsoncs3 provider cache.
`providercache.Cache.PurgeSpace` stored a nil share map. A later `Add` to the
same space panicked. `PurgeSpace` now stores an empty map. And
`providercache.Cache.Remove` checked the wrong `ok` value after it loaded the
space. It now checks the correct value.

https://github.com/owncloud/reva/pull/814
