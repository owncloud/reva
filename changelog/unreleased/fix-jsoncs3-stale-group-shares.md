Bugfix: Remove stale group shares in the jsoncs3 share manager

A stale group share is a share ID in the group cache that has no share in the
provider cache. On each listing of the received shares, the share manager found
the stale ID again. It tried to remove the ID from the received.json file of the
user, but the ID is not in that file. The share manager uploaded received.json
again with the same content on each listing, and the stale ID stayed.

The share manager now removes a stale group share from the group cache. It does
this only when it read the file of the space. A missing space file can be
temporary, so the share manager keeps all entries in that case.

A Remove on the provider cache, the user cache or the group cache no longer
uploads the file when the share is not in it. A Remove on the created cache or
on the group cache no longer panics when the user or group has no entry for the
space of the share, or when the share ID has no delimiter. Before, the panic
stopped the process, also in an unshare.

The share manager now writes a share before the entry for the grantee. When a
write fails, it removes the entries that it wrote. Thus a failed share no longer
blocks a new share with the same grantee.

An unshare now removes the share also when the entry for the grantee cannot be
removed. The gateway removes the storage grant in this case too. The next
listing of the grantee removes the stale entry.

The share manager now removes many shares together, with one upload for each
document. Before, it uploaded each document once for each removed share. A
purge of a space with 20 group shares did 41 uploads and now does 3. A listing
that finds 20 expired shares did 60 uploads and now does 3. The cleanup of
stale shares in a listing also uploads each document once.

When a space is deleted, the share manager now removes the created.json entry
of each share also when the grantee entry cannot be removed. The share manager
now logs the errors of this cleanup. Before, it sent them to a disabled logger.

https://github.com/owncloud/reva/pull/813
