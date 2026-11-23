Bugfix: Fix share update hang on concurrent changes

Updating a share in the jsoncs3 share manager could hang forever when another
writer changed the same space at the same time. The retry after the conflict
tried to take the space lock that the update already held. All later share
operations on that space then hung as well.

The share update now does the whole read, change and write under one space
lock. On a conflict it loads the space again, applies the change to the fresh
share and retries a bounded number of times. The retry also keeps a field mask
update and an update without a share reference intact.

https://github.com/owncloud/reva/pull/841
