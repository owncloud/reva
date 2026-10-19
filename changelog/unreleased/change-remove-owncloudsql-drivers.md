Change: Remove the owncloudsql drivers

We removed the deprecated `owncloudsql` drivers.

The following packages no longer exist:

* `pkg/storage/fs/owncloudsql`
* `pkg/auth/manager/owncloudsql`
* `pkg/user/manager/owncloudsql`
* `pkg/share/manager/owncloudsql`
* `pkg/publicshare/manager/owncloudsql`

The loaders do not register these drivers any more. A configuration that names
one of them now fails at start-up.

https://github.com/owncloud/reva/pull/773
