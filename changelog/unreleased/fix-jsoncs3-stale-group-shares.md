Bugfix: Remove stale group shares in the jsoncs3 share manager

A stale group share is a share ID in the group cache that has no share in the
provider cache. On each listing of the received shares, the share manager found
the stale ID again. It tried to remove the ID from the received.json file of the
user, but the ID is not in that file. The share manager uploaded received.json
again with the same content on each listing, and the stale ID stayed.

The share manager now removes a stale group share from the group cache. Before
the removal, it reads the share again from storage. A Remove on the user cache
or on the group cache no longer uploads the file when the share is not in it.
A Remove on the group cache no longer panics when the group has no entry for
the space of the share.

The share manager now writes a share before the entry for the grantee, and
removes the entry for the grantee before the share. Thus a failed write no
longer leaves a grantee entry without a share.

When a space is deleted, the share manager now removes the created.json entry
of each share also when the grantee entry cannot be removed. The share manager
now logs the errors of this cleanup. Before, it sent them to a disabled logger.

https://github.com/owncloud/reva/pull/813
