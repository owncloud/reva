Bugfix: Stop repeated 404s and trash lookups for groups without shares

When a group had never received a share, its `groups/<id>/received.json` file
did not exist. Every share listing for a member of that group got a 404 for the
file and then listed the trash of the share metadata space to check whether the
file had just been deleted. The same happened for `users/<id>/created.json` of
users who never created a share.

The share cache now creates an empty file after the first 404. Later listings
get a "not modified" answer instead. The empty file is only written if no file
exists, so it never overwrites shares that another instance wrote in the
meantime. If the write fails, the listing still succeeds.

https://github.com/owncloud/reva/pull/836
