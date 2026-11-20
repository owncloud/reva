Bugfix: Reuse HTTP connections in the reva clients

Under high load, the HTTP clients of reva opened a new connection for most
requests. The host ran out of ports. The metadata client and the clients from
GetHTTPClient now keep up to 100 idle connections for each host. They close an
idle connection after 30 seconds.

https://github.com/owncloud/reva/pull/826
https://github.com/owncloud/reva/pull/824
