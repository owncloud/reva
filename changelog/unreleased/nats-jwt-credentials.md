Enhancement: Add NATS JWT authentication with a .creds file

A NATS server in operator (JWT) mode authenticates a client with a user JWT and
an NKey seed. Before this change, reva could connect to NATS only with a
username and a password.

The event streams and the `nats-js` and `nats-js-kv` stores now accept JWT
credentials. You can give the path of a NATS `.creds` file, or the content of
such a file. The content takes precedence over the file. When JWT credentials
are set, reva does not send the username and the password.

- `stream.NatsConfig` has the new fields `CredsFile` and `Creds`.
- `store.Credentials(credsFile, creds)` sets the credentials for a store.
- The cache config has the new keys `cache_creds_file` and `cache_creds`.
- The storageprovider and the dataprovider have the new keys `nats_creds_file`
  and `nats_creds`.
- The publicshares auth manager has the new keys `store_creds_file` and
  `store_creds`.

reva checks the credentials at startup. An invalid value gives a clear error.
The event streams, the publicshares auth manager, the decomposedfs and posix
caches, the gateway caches, and the OCS stat cache stop with this error. Before,
an invalid value for a store only caused an error log, and each store operation
failed later.

https://github.com/owncloud/reva/pull/782
