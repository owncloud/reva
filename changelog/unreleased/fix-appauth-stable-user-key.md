Bugfix: Key app passwords by a stable user id, not the protobuf text form

The json app-auth manager stored each user's app passwords under
`UserId.String()`. That is the protobuf text format, and protobuf-go
deliberately varies its whitespace from one binary to the next so that nobody
depends on it. A bucket written by one build was therefore not found by the
next: after any upgrade (or rebuild) every existing app password silently
stopped working with "password not found", although it was still in the file
and not expired. State files written by several builds held the same user
under several keys.

New app passwords are now always stored under `<idp>|<opaque id>`. Buckets
stored under the old text-format keys are matched by parsing the key back into
a user id, and the first time a user generates, lists, invalidates or uses an
app password their old buckets are moved under the new key and merged. Nothing
is rewritten on startup, a lookup under the new key never consults the old
ones, and existing app passwords keep working across upgrades.

https://github.com/owncloud/reva/pull/755
