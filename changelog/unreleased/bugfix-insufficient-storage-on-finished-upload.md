Bugfix: Report a full space as insufficient storage when an upload is finished

The tus `FinishUpload` implementations did not translate `InsufficientStorage`
into a tusd error, so tusd turned a full space into `500 Internal Server Error`,
which clients cannot tell from a broken server. Both the decomposedfs driver and
the upload coordinator now answer `ERR_INSUFFICIENT_STORAGE` with `507`, as the
initiate path and the plain PUT handlers already do.

https://github.com/owncloud/reva/pull/738
