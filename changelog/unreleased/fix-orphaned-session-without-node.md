Bugfix: Report upload sessions whose node is gone as orphaned

The orphaned upload session filter only matched sessions whose node could not be
read. It did not match sessions whose node does not exist at all, because
ReadNode deliberately swallows a missing node and reports no error.

That left a state which could not be recovered: if a cleanup removed the
orphaned node but did not get as far as removing the session, the remaining
session and its upload data were invisible to the filter and could never be
cleaned up again.

A session is now reported as orphaned when its node cannot be read *or* does not
exist.

https://github.com/owncloud/reva/pull/699
