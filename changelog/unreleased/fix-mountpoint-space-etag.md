Bugfix: Expose the etag of mountpoint spaces

Spaces of type `mountpoint` listed by the shares storage provider did not
carry an etag, so clients could not detect changes in received shares by
comparing the etag of the space root.

The etag of the shared resource is now added to the opaque of each mountpoint
space. Mountpoint spaces and the shares jail now fall back to a stable default
etag when the etag of the share is empty.

https://github.com/owncloud/reva/pull/837
