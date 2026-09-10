Bugfix: Skip non-regular files in the posix driver

The posix storage driver assimilated anything it found in the watched tree,
including symlinks. A symlink placed in the tree out of band therefore became an
ordinary node whose content was whatever it pointed at, and uploading to that
node overwrote the symlink target. Symlinks, sockets and fifos are now skipped,
both when assimilating single items and when warming up the id cache, and blobs
are opened with `O_NOFOLLOW` so an already assimilated file cannot be swapped
for a symlink afterwards. Note that symlinks in the tree are no longer listed at
all. Only the posix driver is affected.

https://github.com/owncloud/reva/pull/731
