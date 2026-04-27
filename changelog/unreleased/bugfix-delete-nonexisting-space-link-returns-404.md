Bugfix: Return 404 when removing a non-existing public or user share

`RemovePublicShare` and `RemoveShare` returned `CODE_INTERNAL` when the
share to be deleted was not found, causing callers to receive a 500 error
instead of the correct 404. Both handlers now propagate `IsNotFound`
errors from the share manager as `CODE_NOT_FOUND`.

https://github.com/owncloud/reva/pull/581
