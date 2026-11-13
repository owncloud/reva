Bugfix: Return 425 from COPY while the source is in postprocessing

A WebDAV COPY of a file that was still in postprocessing failed with 500
Internal Server Error, because the internal download of the source returned
425 Too Early. The COPY now returns 425 Too Early as well, so clients know
they can retry the request later, the same way GET and HEAD already do.
The error responses of COPY also no longer try to write a second status code.

https://github.com/owncloud/reva/pull/808
