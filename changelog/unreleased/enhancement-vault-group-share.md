Enhancement: Allow group grantees on vault resources

The gateway now accepts a group as the grantee of a vault share or of a vault
space membership. Before this change, the gateway denied all group grantees.
The gateway does not check the VaultMode permission for a group, because role
assignments belong to single accounts. The notification services filter the
group members by the VaultMode permission instead.

The auth interceptor has a new option `vault_mode_required`. When the option is
on, the interceptor denies each request from a user who does not hold the
VaultMode permission. Service accounts are not checked. The interceptor caches
the answer for each user for 30 seconds. Use `vault_mode_cache_seconds` and
`vault_mode_cache_size` to change the cache. An error during the check denies
the request. The vault storage provider uses this option, so a group member
without the VaultMode permission cannot open a vault resource.

https://github.com/owncloud/reva/pull/787
