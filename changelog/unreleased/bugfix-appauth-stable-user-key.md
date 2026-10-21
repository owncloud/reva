Bugfix: Key app passwords by a stable user id

The JSON app auth manager stored the app passwords of each user under the
protobuf text form of the user id. Protobuf-go changes the whitespace of this
text between builds. After an upgrade, the manager did not find the stored app
passwords any more. Every request with an existing app password failed with
"password not found".

The manager now stores the app passwords under the key `<idp>|<opaque id>`. At
start, it moves the buckets with the old keys to the new key. It merges buckets
of one user that differ only in whitespace. It writes the file only when it
changes a key. Existing app passwords keep working after the upgrade.

https://github.com/owncloud/reva/pull/777
