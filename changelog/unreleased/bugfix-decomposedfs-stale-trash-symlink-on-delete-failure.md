Bugfix: Remove stale trash symlink when decomposedfs Delete fails mid-operation

`Tree.Delete` creates a trash symlink before renaming the node. If any
subsequent step failed (`os.Rename`, `MetadataBackend().Rename`, or
`os.Remove` of the parent-dir entry) the symlink was not removed during
rollback. The dangling symlink permanently blocked future delete attempts
on the same node with a `file exists` error on the next `os.Symlink` call.

All three failure paths now remove the trash symlink as part of rollback.
The third path additionally reverts the node rename and metadata rename in
reverse order.

https://github.com/owncloud/reva/pull/723
