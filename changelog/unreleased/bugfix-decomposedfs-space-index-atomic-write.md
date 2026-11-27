Bugfix: Replace the space index files of decomposedfs atomically

Decomposedfs truncated an index file under `indexes/` (for example
`by-type/project.mpk` or `by-user-id/<id>.mpk`) and wrote it again in place on
every update. A process that died between truncating and writing left an empty
or unreadable index behind, on any filesystem, and every later update of an
unreadable index failed. With several storage-users instances on a shared
CephFS volume mounted with the kernel client, an instance could also read an
index up to a stale size and drop the entries of the other instance: new spaces
were missing from the space lists of their members and from the admin list.

The index is now written to a temporary file in the same directory and renamed
over the old one, under a separate lock file `<index>.mpk.mlock`, as the node
metadata already does. Within one process only one writer per index waits for
the file lock.

All instances that share a storage have to be updated at the same time: older
versions lock the index file itself and do not exclude newer ones.

https://github.com/owncloud/reva/pull/868
https://github.com/owncloud/reva/issues/867
