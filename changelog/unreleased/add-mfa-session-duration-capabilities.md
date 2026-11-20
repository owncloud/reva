Enhancement: Add MFA session duration enforcement capabilities

Added the `enforce_session_duration` and `issued_at_claim` fields to the MFA
capabilities, so clients can ask for MFA again once it is older than the
session duration.

https://github.com/owncloud/reva/pull/823
