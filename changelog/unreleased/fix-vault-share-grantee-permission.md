Bugfix: Only share vault resources with grantees allowed to use vault mode

Vault ("Safe") resources could be shared with any grantee, including users that lack
the `VaultMode.ReadWriteEnabled` permission and can therefore never enter vault mode.
Such a grant was useless to the grantee but still produced a share notification that
disclosed the resource name to them.

Share creation on a vault resource now verifies that the grantee holds the vault mode
permission and is denied otherwise. The check is applied in the gateway ahead of the
space root branch, so it covers plain shares as well as space memberships, and it fails
closed if the permission cannot be determined. Group grantees are rejected, because
settings role assignments are held by individual accounts and group membership can
change after the share has been created.

https://github.com/owncloud/reva/pull/727
