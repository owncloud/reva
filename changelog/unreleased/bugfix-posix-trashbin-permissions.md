Bugfix: Check permissions in the trashbin of the posix driver

The trashbin of the posix storage driver did not check any permissions when
listing, restoring, purging or emptying the trash. An authenticated user who
knew the id of a space could read, restore or permanently delete the trashed
content of a space they are not a member of. Only the posix driver was affected.
The decomposedfs based drivers (ocis, s3ng) already checked the permissions of the space.

All four operations now assemble the trash permissions of the space and are
gated on `ListRecycle`, `RestoreRecycleItem` and `PurgeRecycle` respectively.
Callers who are not allowed to stat the space receive a not found error instead
of a permission denied error, so that the existence of a space is not
disclosed. Note that emptying the trash requires `PurgeRecycle`, which means a
space viewer can still browse the trash but can no longer empty it.

https://github.com/owncloud/reva/pull/729