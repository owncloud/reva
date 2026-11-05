Bugfix: Enforce public link authorization when changing a link's type

Changing a share link's type is now subject to the same authorization checks as
creating a link. Raising an internal link to a public link is only allowed when
public sharing is enabled and the user is permitted to create public links and to
reshare the resource.

https://github.com/owncloud/reva/pull/795
