https://github.com/owncloud/reva/pull/780Bugfix: Don't log the public link password

When persisting a new public link failed, the publicshareprovider logged the
whole create request, including the link password in plain text. The error
log now only contains the resource id, the link permissions and whether a
password was set.

https://github.com/owncloud/reva/pull/780
