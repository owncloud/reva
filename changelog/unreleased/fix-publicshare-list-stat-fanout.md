Bugfix: Stop statting every public link when listing shares unfiltered

ListPublicShares issued one gateway Stat per public link not created by the
calling user in order to check the ListGrants permission. On tenants with a few
thousand links this exceeded the request deadline and the shares list returned
nothing. The permission check itself is unchanged, so every user sees exactly
the same links as before. Instead the manager now stats each distinct resource
at most once per request, caching allowed and denied answers alike, and runs
those stats through a bounded worker pool rather than one after another. When
the caller's own deadline is about to expire the remaining stats are abandoned
and a partial list is returned with a warning, instead of the whole request
failing.

https://github.com/owncloud/reva/pull/712
