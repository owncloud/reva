Bugfix: Exempt service accounts from MFA enforcement in vault mode

Service accounts (UserType_USER_TYPE_SERVICE) are now exempt from MFA checks
in the gRPC auth interceptor. Previously, internal services like userlog and
notifications failed to stat vault resources because their service account
tokens lacked MFA context, causing share notifications to be silently discarded.

https://github.com/owncloud/reva/pull/TODO
