# Changelog

## [v1.1.2] - 2023-04-29

### Changed

- Reading instanceID list form globalDB's `instance` collection then use the list of instanceIDs to filter unauthenticated requests directed toward non-listed instances.
- Improve logging by using the custom logger for all log lines.
- Hardening email validation rules to prevent using invalid emails.

## [v1.1.1] - 2022-10-28

### Changed

- Increase minimum go version, since it's required by dependecies.

## [v1.1.0] - 2022-10-28

### Added

- Makefile accepts TAG variable to customize the version tag
- [PR #14](https://github.com/influenzanet/user-management-service/pull/14) make token duration configurable using environment variables
- [PR #12](https://github.com/influenzanet/user-management-service/pull/12) implements the possibility to use weighted probablities for weekday assignments. Default behaviour unchanged.

### Changed

- Improve logging by using the custom logger at more places.
- Updating project dependencies.

## [v1.0.0] - 2022-03-08

### Added

- Possibility to send the registration email to unverified user accounts a second time, with a configurable time threshold (defined in seconds). The new environment variable for this (`SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER`) must be set. If the reminder should not be used, simply set this value to a larger number than the value used to clean up unverified users. The check if users should receive a verification reminder, will run with the same frequency as the clean-up task.
- `tools/db_config_tester`, a small program that can be used to generate dummy users and benchmark how long it takes to iterate over them using the `PerformActionForUsers` db service method.

### Changed

- `PerfomActionForUsers` improved context handling to avoid unnecessary timeouts for long lasting jobs. Also now returned error of the callback will stop the iteration. Improved logging output of this method.
- Modified tool for creating admin users, to accept username and password throught command line input, hiding the password from history.
- updated gRPC version and proto build tools

## [v0.20.4] - 2021-12-07

- Loglevel can be configured. Use the environment variable `LOG_LEVEL` to select which level should be applied. Possible values are: `debug info warning error`.
- Updated JWT lib to most recent version on v4 track.

## [v0.20.3] - 2021-12-07

### Changed

- CreateUser: accepts a configurable value for account confirmation time (when migrating users from previous system and does not need confirmation). Also can set account created at time from the API request.
- Optimise TempToken cleanup by only performing the action only once in ten minutes and not on every request. Add debug log message when TempTokens are cleaned up.
- Project dependencies updated.

## [v0.20.2] - 2021-07-27

### Security Update

- Migrating to `github.com/golang-jwt/jwt`
- Updating other dependencies

## [v0.20.1] - 2021-07-01

### Changed

- LoginWithExternalIDP: user newly created user object to handle first time login.

## [v0.20.0] - 2021-07-01

### Added

- New endpoint: LoginWithExternalIDP. This method handles logic for login process when a user is using an external identity provider (IDP) to login. If user did not exist in the system before, an account with type "external" will be created. If an account of type "email" already exists, the method will fail.

### Changed

- LoginWithEmail endpoint will check account type, if external account is accessed through this endpoint, login will fail - use the external IDP instead.
- minor code improvements to use globally defined constants instead of locally hard-coded strings

## [v0.19.4] - 2021-06-16

### Changed

- Changing endpoint for auto verificiation code generation through temp token received by email. There were occasional reports of people not able to login with email link. After catching one of such instances, it is likely that somehow a double request to that endpoint caused the replacement of the verification code. With this update, if the user identified by temp token, has a recently generated valid verification code in the DB, we won't replace it, but send this one back (agian).

## [v0.19.3] - 2021-06-03

### Added

- New tool to create an admin user. This is located in [here](tools/create-admin-user)

### Changed

- Include user role "service account", when looking up non-participant users.
- Adding context for timer event's run method, to prepare logic for graceful shutdown.
- gRPC endpoint for creating a new user (`CreateUser`), accept a list of profile names that is then used to create profiles. The first profile name will be assigned to the main profile. If the list is empty, the blurred email address will be used for the main profile as before.
