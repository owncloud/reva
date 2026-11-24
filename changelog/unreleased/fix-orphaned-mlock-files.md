Bugfix: Remove orphaned metadata files on delete and purge

The decomposedfs left files on the disk when a user deleted a file or purged it from the trash bin. Each orphaned file uses an inode.

- The delete operation now removes the `.mlock` file of the node.
- The metadata backends now remove the `.mlock` file when they purge a node.
- The purge operation now finds the revisions of a node from the trash bin. It removes the revision files, their metadata files, and their blobs.

https://github.com/owncloud/reva/pull/849
https://github.com/owncloud/ocis/issues/13476
