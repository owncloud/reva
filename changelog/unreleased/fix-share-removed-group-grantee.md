Bugfix: Set the group grantee in the ShareRemoved event

The ShareRemoved event did not contain the group of a removed group share.
The events middleware read the group ID into the user ID field. Thus the
notification services sent the "Resource unshared" notification to no member
of the group. The event now contains the group ID in the correct field.

https://github.com/owncloud/reva/pull/787
