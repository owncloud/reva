Enhancement: Show the correct service name and the metadata calls in traces

We've changed how reva starts the spans of the decomposedfs, the gRPC client
pool, the data gateway, and the stat cache. Each span now uses the tracer
provider of its request context. Before, these spans used the global tracer
provider. In the single binary, each service sets the global provider when it
starts. The last service wins. So these spans showed the service name of
another service.

The metadata storage and the jsoncs3 share caches now keep their calls in the
trace of the request. The metadata calls show their result in the span. A
"Grab lock" span shows the wait for a cache lock. When tracing is off, the new
spans cost almost nothing.

https://github.com/owncloud/reva/pull/871
