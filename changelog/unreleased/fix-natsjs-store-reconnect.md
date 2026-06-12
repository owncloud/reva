Bugfix: Survive permanently-closed NATS connections in the nats-js store

The nats-js and nats-js-kv store backends used the NATS client defaults,
which give up reconnecting after 60 attempts (~2 minutes). Any NATS outage
longer than that left the client permanently closed, surfacing as
`nats: connection closed` on every subsequent cache operation until the
process was restarted.

The store now reconnects forever (`MaxReconnect = -1`) with a 5s backoff and
logs connection state transitions (disconnect/reconnect/close), which were
previously silent.

In addition, `loadAttributes` in the decomposedfs messagepack metadata backend
no longer fails a successful disk read when writing the attributes back into
the cache fails: the cache write-back error is logged and the data read from
disk is returned.

https://github.com/owncloud/reva/pull/628
