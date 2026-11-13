Bugfix: Treat a missing blob as deleted in s3ng

Deleting a blob that no longer exists failed on S3-compatible stores that
answer 404 NoSuchKey for a missing key, such as Google Cloud Storage (AWS S3
answers 204). During a trash purge the error aborted the depth-first removal
part-way: the trash entry was kept, so the folder could later be restored with
dangling children. A missing object is now treated as already deleted; every
other error still fails.

https://github.com/owncloud/reva/pull/785
https://github.com/owncloud/reva/issues/784
