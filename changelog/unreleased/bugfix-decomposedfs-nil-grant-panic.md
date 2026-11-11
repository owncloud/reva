Bugfix: Fix a crash when decomposedfs reads permissions

The storage provider could stop when it read the permissions of a user.
We changed the code. The code now checks the result of a read before it uses
the result. The code also rejects invalid permission data and does not stop.

https://github.com/owncloud/reva/pull/804
