Bugfix: Fix a panic when a grpc server stops before it starts to serve

The `rgrpc` server created its grpc server only after all service
constructors returned. Some constructors are slow, for example when they
retry a NATS connection. When the process got a stop signal during this
time, `Stop` and `GracefulStop` called the nil grpc server and panicked.
The panic replaced the normal shutdown with a crash. It also hid the real
reason for the shutdown.

`Stop` and `GracefulStop` now work before `Start` and while `Start` creates
the services. A mutex protects the fields that `Start` and the stop
functions share. When a stop comes first, `Start` closes the services and
returns without serving. Each service is now closed one time only.

https://github.com/owncloud/reva/pull/814
