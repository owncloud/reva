Enhancement: Allow group grantees on vault resources

The gateway now accepts a group as the grantee of a vault share or of a vault
space membership. Before this change, the gateway denied all group grantees.
The gateway does not check the VaultMode permission for a group, because role
assignments belong to single accounts. The notification services filter the
group members by the VaultMode permission instead. Each member still needs MFA
to open the vault resource.

https://github.com/owncloud/reva/pull/787
