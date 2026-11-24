Bugfix: Remove the files of a new node when its creation fails

The decomposedfs left a node file and its metadata file on the disk when the
creation of a new file failed. This happened when the quota was exceeded, or
when a parallel upload created a file with the same name first. No name linked
to these files, and each file used an inode.

InitNewNode now removes the node file, its metadata file and its lock file
after a failure. The name link stays, because it belongs to the other upload.

https://github.com/owncloud/reva/pull/852
https://github.com/owncloud/ocis/issues/13476
