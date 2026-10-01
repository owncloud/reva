Bugfix: Key app passwords by a stable user id, not the protobuf text form

The json app-auth manager stored each user's app passwords under
`UserId.String()`. That is the protobuf text format, and protobuf-go
deliberately varies its whitespace from one binary to the next so that nobody
depends on it. A bucket written by one build was therefore not found by the
next: after any upgrade (or rebuild) every existing app password silently
stopped working with "password not found", although it was still in the file
and not expired. State files written by several builds held the same user
under several keys.

Buckets are now keyed by `<idp>|<opaque id>`. On load, buckets stored under
the old text-format keys are parsed, re-keyed and merged, and the file is
rewritten, so existing app passwords keep working across upgrades.

https://github.com/owncloud/reva/pull/755
