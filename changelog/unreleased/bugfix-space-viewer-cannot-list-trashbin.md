Bugfix: Space viewers can no longer list the trash bin

The space viewer role ("Can view") could list the trash bin of a space and see
the names and deletion times of deleted items, while the space editor roles
without trash bin access could not. The space viewer role no longer has the
permission to list the trash bin. Permanently deleting items and purging the
trash bin stay forbidden.

Space viewers without the trash bin permission are still recognised as viewers
and keep the download permission, they are not treated as secure viewers.

Members that got the space viewer role before this change keep the trash bin
permission until their role is assigned again.

https://github.com/owncloud/reva/pull/775
