Change: Remove the posix driver

We removed the deprecated `posix` storage driver `pkg/storage/fs/posix`.

The loader does not register the driver any more. A configuration that names
it now fails at start-up.

The decomposedfs tests now use `pkg/storage/utils/decomposedfs/timemanager`
instead of the posix time manager. The posix acceptance test pipelines and
configuration files are also removed.

https://github.com/owncloud/reva/pull/773
