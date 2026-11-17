Bugfix: Remove stale group shares in the jsoncs3 share manager

A stale group share is a share ID in the group cache that has no share in the
provider cache. On each listing of the received shares, the share manager found
the stale ID again. It tried to remove the ID from the received.json file of the
user, but the ID is not in that file. The share manager uploaded received.json
again with the same content on each listing, and the stale ID stayed.

The share manager now removes a stale group share from the group cache. It does
this only when it read the file of the space. A missing space file can be
temporary, so the share manager keeps all entries in that case. The share
manager reads each share from the same copy of the space file. Before, a
parallel listing could empty the cache between two reads, and then the
cleanup removed valid shares from the group cache.

The share manager removes a stale entry only after a grace period of 5 minutes.
An older version writes the grantee entry and the share at the same time.
During a rolling upgrade, a listing can find the grantee entry before the share
is on storage. Before, the listing removed this entry, and no group member saw
the share.

A Remove on the provider cache, the user cache or the group cache no longer
uploads the file when the share is not in it. A Remove on the created cache or
on the group cache no longer panics when the user or group has no entry for the
space of the share, or when the share ID has no delimiter. Before, the panic
stopped the process, also in an unshare.

When the upload of a Remove fails, the cache now reads the file again on the
next call. Before, the cache kept the share out of memory, but the share stayed
in the file. A failed upload does not change the etag, so a sync did not bring
the share back. A retry then found nothing to remove, did not upload, and
reported success. In the user cache, this also occurred inside one Remove after
a temporary upload error.

The same applies to an Add. Before, a failed upload of an Add kept the new
entry in memory. Then the pod listed a share or a share state that was not in
the file, and the next write for the same document uploaded it. For example,
a client got an error when it accepted a share, but this pod showed the share
as accepted.

The share manager now writes a share before the entry for the grantee. When a
write fails, it removes the entries that it wrote, also the entry for the
grantee. The removal also runs when the request was cancelled. Thus a failed
share no longer blocks a new share with the same grantee, and it leaves no
stale grantee entry.

An unshare now removes the share also when the entry for the grantee cannot be
removed. The gateway removes the storage grant in this case too. The next
listing of the grantee removes the stale entry.

The share manager now removes many shares together, with one upload for each
document. Before, it uploaded each document once for each removed share. A
purge of a space with 20 group shares did 41 uploads and now does 3. A listing
that finds 20 expired shares did 60 uploads and now does 3. A listing removes
the expired shares of all spaces together, after it read all spaces. The
cleanup of stale shares in a listing also uploads each document once.

When a space is deleted, the share manager now removes the created.json entry
of each share also when the grantee entry cannot be removed. The share manager
now logs the errors of this cleanup. Before, it sent them to a disabled logger.

Each log line of a share listing now has the fields of one share only. Before,
the listing added the fields of each share to the same logger. A later line
then had the fields of all earlier shares, and a log search found the wrong
share.

https://github.com/owncloud/reva/pull/813
