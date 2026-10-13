Enhancement: Add offline purge of orphaned received shares to the jsoncs3 share manager

The jsoncs3 share manager can accumulate orphaned received-share entries when a
space is deleted but the per-user received.json blocks are not cleaned up (for
example after a missed SpaceDeleted event). These orphans are removed inline on
ListReceivedShares, but that happens on the request hot path and can race a
client deadline.

This adds `Manager.PurgeOrphanedReceivedShares`, which enumerates users, detects
received-share entries whose share no longer exists in the provider cache and
removes them in a single batched persist per user and space
(`receivedsharecache.Cache.RemoveSpaceShares`). It can be scoped to a single
space and supports a dry-run mode. An entry is only removed when the share is
confirmed absent after a successful space listing, so live shares are never
removed on a transient read failure.

https://github.com/owncloud/reva/pull/656
