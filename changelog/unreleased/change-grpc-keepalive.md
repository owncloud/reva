Change: [OCISDEV-1433] Replace GRPC_MAX_CONNECTION_AGE with client keepalive

The grpc clients now send a keepalive ping while a request is in flight and fail
the requests on a connection whose peer stops answering, instead of waiting for
as long as the caller allows. GRPC_CLIENT_KEEPALIVE_TIME (20s) and
GRPC_CLIENT_KEEPALIVE_TIMEOUT (10s) tune this; setting the former to 0 disables
the pings.

GRPC_MAX_CONNECTION_AGE has been removed. It only closed healthy connections on
a timer, never ended a request that was already in flight, and silently did
nothing when its value had no unit suffix.

https://github.com/owncloud/reva/pull/748
