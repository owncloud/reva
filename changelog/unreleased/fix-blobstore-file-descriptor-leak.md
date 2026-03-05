Bugfix: Fix file descriptor leak and ensure blob data persistence

Add proper file descriptor cleanup and disk synchronization to prevent data loss when uploading
blobs. The ocis blobstore was missing a deferred Close() call, causing file descriptor leaks and
potential data loss when writes were interrupted. Both ocis and posix blobstores were missing
Sync() calls to ensure data is flushed to disk.

This fixes an issue where received.json files would sometimes remain in the uploads/ folder
instead of being properly moved to the blobs/ directory, causing share creation to silently
fail without error logs.

https://github.com/owncloud/reva/pull/550
