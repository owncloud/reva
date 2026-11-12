Enhancement: Move the vault checks into a separate interceptor

The new gRPC interceptor `vault` protects the vault storage provider. It runs
after the auth interceptor. The auth interceptor now only authenticates the
request.

The `vault` interceptor has these options:

- `mfa_required` denies each user whose token does not carry MFA. This option
  replaces the option `mfa_enabled` of the auth interceptor.
- `vault_mode_required` denies each user who does not hold the VaultMode
  permission. This option was an option of the auth interceptor before.
- `vault_mode_cache_size` and `vault_mode_cache_seconds` configure the cache
  of the VaultMode answers.
- `priority` sets the position of the interceptor in the chain.

Service accounts are not checked. Each interceptor now has its own cache.
Before this change, all auth interceptors in one process used the cache
settings of the first interceptor.

The new package `pkg/vault` holds the VaultMode check. The gateway, the
interceptor, and other services, for example oCIS, use the same check.

https://github.com/owncloud/reva/pull/809
