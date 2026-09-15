Bugfix: Report a full space as insufficient storage when an upload is finished

`FinishUpload` only translated `AlreadyExists` and `Aborted` into tusd errors,
so tusd turned a full space into `500 Internal Server Error`, which clients
cannot tell from a broken server. It now translates `InsufficientStorage` into
`ERR_INSUFFICIENT_STORAGE` with `507`, as the initiate path and the plain PUT
handlers already do.

https://github.com/owncloud/reva/pull/738
