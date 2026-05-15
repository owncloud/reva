Security: Enforce postprocessing quarantine on the storage download path

The "still being processed" quarantine (used e.g. for antivirus scanning) was
only enforced by the ocdav GET/HEAD/PROPFIND handlers via a separate Stat
call. `Decomposedfs.Download` and `Decomposedfs.DownloadRevision` did not
check the node's processing state, so the datagateway `/data` endpoint and
revision downloads could serve unscanned/in-process content, and the ocdav
path was racy. The processing state is now enforced at the storage layer and
the data transfer handler returns `425 Too Early` accordingly.

https://github.com/owncloud/reva/pull/596
