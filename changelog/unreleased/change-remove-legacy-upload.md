Change: [OCISDEV-901] [OCISDEV-1382] Remove the legacy upload path from storage drivers

Storage drivers no longer handle uploads themselves. `InitiateUpload` and
`Upload` have been removed from the `storage.FS` interface, together with the
tus methods drivers used to implement (`UseIn`, `NewUpload`, `GetUpload` and
the `As...Upload` helpers) and the `ComposableFS` and deprecated
`UploadsManager` interfaces. The upload coordinator in `pkg/upload` has been
handling every upload since the coordinator switchover, so this only removes
code that was no longer called. `storage.UploadRequest` and
`storage.UploadFinishedFunc` now live in the coordinator package as
`upload.Request` and `upload.FinishedFunc`.

Upload sessions are now listed through the coordinator only.
`ListUploadSessions` has been removed from the drivers and the
`storage.UploadSessionLister` interface is gone; use
`upload.Coordinator.ListUploadSessions` instead, which supports the same
filters, including the orphaned filter. The old decomposedfs session store in
`pkg/storage/utils/decomposedfs/upload` has been deleted as well. The
coordinator reads the same session files under `<root>/uploads/`, so uploads
that are in flight during an update are not affected.

The decomposedfs-based drivers (ocis, s3ng and posix), nextcloud, kiteworks and
the OCM received-shares storage support uploads through the coordinator. The
deprecated drivers (cephfs, eos, eoshome, eosgrpc, eosgrpchome, local,
localhome, owncloudsql, s3 and hello) do not: their `CommitUpload` returns a
"not supported" error, so uploads to them fail.

https://github.com/owncloud/reva/pull/725
