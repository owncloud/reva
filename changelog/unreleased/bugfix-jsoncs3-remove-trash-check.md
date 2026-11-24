Bugfix: Remove the trash lookup after a missing share cache file

When a jsoncs3 share cache file did not exist, the share manager got a 404 for
the file. Then it listed the trash of the share metadata space to check whether
the file had just been deleted. This check could never find the file on
decomposedfs, because it compared a path that starts with `./` with a trash
origin that starts with `/`. So the check always reported "not deleted", and
each 404 cost two more calls to the storage: an authentication and a trash
listing.

The share manager does not look at the trash anymore. The behavior does not
change, because the check never matched. A 404 for a missing file now costs the
same as a "not modified" answer. This fix applies to the created shares, to the
received shares of users and groups, and to the shares of a space.

https://github.com/owncloud/reva/pull/848
