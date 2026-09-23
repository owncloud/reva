Bugfix: Announce the vault storage provider id on every capabilities response

`vault_storage_provider` was only filled in by the `?vault=true` branch of
`GetCapabilities`, so a client that fetched capabilities without that query
parameter received `vault.enabled: true` together with an empty provider id.
Clients running outside the vault use that id to recognize vault resources by
their storage provider, for example to redirect a vault private link into the
vault scope or to keep vault notifications out of the regular view. With an
empty value those comparisons never matched and a permanent link to a vault
resource, opened outside the vault, failed to resolve.

The id is a fixed constant, not a deployment secret, so it is now set once in
`Init` whenever vault mode is enabled and is therefore part of every
capabilities response. The `?vault=true` branch keeps its remaining job of
disabling public sharing and federation for the vault scope, and no longer
mutates the handler's shared capabilities struct, which had leaked the id into
subsequent non-vault responses once any vault request had been served.

https://github.com/owncloud/reva/pull/747
