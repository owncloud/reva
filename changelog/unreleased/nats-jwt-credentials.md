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
- `stream.NatsOptions(cfg)` returns the `nats.Option` values for the
  authentication and the TLS settings. The event streams use it. Other NATS
  connections, for example health checks, can use the same rules.
- `natsauth.Option(credsFile, creds)` returns the credentials option without a
  check. For a file, the option reads the file only when the connection
  connects. Use it after a reconnect, when a rotated file can be partly written.

reva checks the credentials at startup. An invalid value gives a clear error.
The event streams, the publicshares auth manager, the decomposedfs and posix
caches, the gateway caches, and the OCS stat cache stop with this error. Before,
an invalid value for a store only caused an error log, and each store operation
failed later. reva checks the credentials of a store or a cache only when its
type is `nats-js` or `nats-js-kv`. Other types do not use the credentials.

https://github.com/owncloud/reva/pull/782
