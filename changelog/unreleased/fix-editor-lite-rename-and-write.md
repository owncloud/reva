Bugfix: Allow renaming and editing files with the editor-lite role

The editor-lite role ("Can edit" in the web UI) grants Move and
InitiateFileUpload, but the OCS permissions derived from it did not include
write. Clients pick the actions they offer from the resulting WebDAV
permissions string, which therefore lacked "NV" (rename) and "W" (overwrite),
so sharees could neither rename files nor change their contents inside a shared
folder even though the storage layer accepted both. Move now implies write. The
role still grants neither delete nor version history.

https://github.com/owncloud/reva/pull/689
https://github.com/owncloud/ocis/issues/11977
