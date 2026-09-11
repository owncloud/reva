Bugfix: Skip public shares with nil resource_id in ListPublicShares

ListPublicShares panicked when a persisted public share had a nil
resource_id, since the resource_id fields were dereferenced without a nil
check while building the cache key. Such a share is now skipped, with a
warning logged, instead of crashing the request.

https://github.com/owncloud/reva/pull/735
