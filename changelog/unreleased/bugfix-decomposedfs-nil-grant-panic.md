Bugfix: Fix a crash when decomposedfs reads permissions

The storage provider could stop when it read the permissions of a user.
A grant with invalid data on disk caused a panic. The panic stopped the full
storage provider process.

We changed the code:

- The code checks the result of a grant read before it uses the result.
- The code checks for a missing grant before it checks the expiration.
- The code returns an error for an invalid grant value or grantee name.
- The code keeps the full value of a grant field that contains an equals sign.

The storage provider now logs the invalid grant and does not stop.

https://github.com/owncloud/reva/pull/804
