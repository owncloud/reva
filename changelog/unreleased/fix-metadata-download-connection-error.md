Bugfix: [OCISDEV-1714] Keep the connection error of a metadata download

The cs3 metadata storage changed each error of the InitiateFileDownload call
to NotFound. This call does not read the file. It only returns the download
URL. So an error from this call is a connection error, for example from a dead
replica. A caller that gets NotFound continues as if the file does not exist.
The settings service then returned an empty role list, and the proxy replaced
the role of the user with the default role. The jsoncs3 share manager reset a
share cache to empty.

The download now returns the connection error. A non-OK status of the call
gives the matching typed error. A missing file still gives NotFound.

https://github.com/owncloud/reva/pull/820
