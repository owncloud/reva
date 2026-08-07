Bugfix: Allow renaming and editing files with the editor-lite role

The editor-lite role ("Can edit" in the web UI) grants Move and
InitiateFileUpload, but sharees could neither rename a file inside a shared
folder nor see the rename and overwrite actions offered in the web UI.

Two things were wrong. The persisted ACE format had no flag of its own for
Move: it folded Move into the same "w" flag as InitiateFileUpload and
recovered it on read by assuming a grant may move whenever it may write,
download and delete. A grant that may rename but not delete, which is exactly
editor-lite, therefore lost Move on the way to disk, so the storage layer
refused the rename and propfind reported the grant as a create-only uploader
with an empty WebDAV permissions string. Move is now persisted as its own "m"
flag; grants written before it existed carry no "m" and keep using the old
inference. On top of that, the OCS permissions derived from the role did not
include write, so the WebDAV permissions string lacked "NV" (rename) and "W"
(overwrite) even for a grant that had kept its Move. Move now implies write
for grants that do not carry delete, which leaves the OCS permissions of the
deletable roles unchanged.

The role still grants neither delete nor version history.

https://github.com/owncloud/reva/pull/689
https://github.com/owncloud/ocis/issues/11977
