Bugfix: Cache decoded node attributes in the file metadata cache

The file metadata cache used by decomposedfs now keeps a bounded, process-local
layer of already decoded node attributes. Reads still fetch the serialized
attributes from the configured cache store, but skip decoding them again when
the stored bytes are unchanged. This roughly halves the cost of a metadata
cache hit and reduces its allocations by about 75%, while returning exactly the
same data as before, also when the cache store is shared between processes.

The size of the layer can be limited with the new `cache_decoded_attributes_max_entries`
and `cache_decoded_attributes_max_bytes` cache options; a negative value disables it.
It is always disabled for the `noop` store.

https://github.com/owncloud/reva/pull/864
https://github.com/owncloud/reva/pull/840
