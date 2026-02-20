Bugfix: Fix WOPI open-with-web due to app registry ordering

Made app selection deterministic when no app is specified: the system now uses the configured default app for the file type, or picks the first provider sorted by priority then name, ensuring users get consistent results instead of random app selection based on registration timing.

https://github.com/owncloud/reva/pull/540
