Bugfix: Add app fallback

When no app is specified in the open with web request, we now fallback to the first app provider found for the resource.
If no app providers are found, we return an error.

https://github.com/owncloud/reva/pull/443
