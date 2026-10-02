Enhancement: receivedsharecache: retry on transient storage errors in CAS loop

The received share cache now retries the compare-and-swap persist loop when
the storage returns transient errors (TooEarly, AlreadyExists, Aborted,
PreconditionFailed), avoiding spurious failures under concurrent writes.

https://github.com/owncloud/reva/pull/657
