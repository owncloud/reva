Bugfix: Ask for a login, not a password, when an internal link is opened

Since internal links can no longer be accessed anonymously, the publicshares
auth manager refuses them. ocdav answered that refusal with
`ERR_MISSING_BASIC_AUTH`, so Web showed "This resource is password-protected"
for a link that has no password and never sent the user to the login.

ocdav now recognises the refusal. An anonymous request gets a 401 with
`ERR_MISSING_BEARER_AUTH` again, so Web sends the user to the login and then to
the private link. A logged-in user without access to the resource gets a 404.
Internal links still cannot be accessed anonymously.

https://github.com/owncloud/reva/pull/XXX
