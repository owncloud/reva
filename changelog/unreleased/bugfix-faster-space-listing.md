Enhancement: Reduce the cost of listing storage spaces

Listing storage spaces in decomposedfs no longer calls lstat on the root of every
space to find out its type, because space roots are always directories. Parsing
the grants of a space no longer sets up a csv reader for every grant, which
removes most of the memory allocated while listing spaces.

https://github.com/owncloud/reva/pull/865
https://github.com/owncloud/reva/pull/839
