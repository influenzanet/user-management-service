# WhatsApp verification rate limit — concurrency tests

Each test asserts a **property the endpoint is supposed to guarantee**

- no more than `allowedPhoneVerificationAttempts` messages leave per window;
- every message that left is still counted afterwards.

## Running them

Requires `docker` or `podman` (running) and a Go toolchain.

```sh
./run-tests.sh
```

The script picks whichever runtime is available, preferring `docker`; set `CONTAINER_RUNTIME=podman`
to force one. It starts a throwaway MongoDB on port 27018, runs the tests, then removes the
container.
It also runs the repository's pre-existing `TestAddPhoneNumberEndpoint`, so a regression
introduced by the fix would show up in the same run.

## Where the test file lives

`pkg/grpc/service/account_management_ratelimit_concurrency_test.go`.

It is `package service` and uses unexported identifiers (`userManagementServer`,
`allowedPhoneVerificationAttempts`, the `testUserDBService` helpers from `server_test.go`), so Go
requires it to sit next to the code under test. Only the runner and this note live here.

## The tests

| Test | What it forces |
| --- | --- |
| `TestAddPhoneNumberRateLimitUnderConcurrency` | 10 simultaneous requests, all held inside the send until every one has arrived |
| `TestAddPhoneNumberHonoursRemainingBudgetWhenRequestsOverlap` | Deterministic 2-request sequence against a user with exactly one send left |
| `TestAddPhoneNumberBurstReachesDistinctNumbers` | The same burst, but each request nominates a different destination number |
| `TestAddPhoneNumberRateLimitWithRealisticSendLatency` | 6 requests fired 50 ms apart against a 400 ms send, i.e. no simultaneity at all |
