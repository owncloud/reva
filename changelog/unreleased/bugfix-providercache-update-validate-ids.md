Bugfix: Reject empty ids in the provider cache Update

An Update on the provider cache now returns an error when the storage id, the
space id or the share id is empty, as an Add does. Before, it took the lock of
an empty space id and returned NotFound. New tests check that an UpdateShare
returns and keeps the change when the first upload hits a CAS conflict.

https://github.com/owncloud/reva/pull/847
