# WhatsApp verification rate limit — cross-endpoint tests (N-3)

Follow-up to `test/whatsapp-rate-limit-concurrency/` (N-2).

The reservation added in `e1080a6` is atomic, so the four N-2 tests now pass: `AddPhoneNumber`
called against itself, at any concurrency, cannot exceed the budget.

These tests assert the **same two invariants**

- no more than `allowedPhoneVerificationAttempts` messages leave per window;
- every message that left is still counted afterwards;

but without confining the traffic to `AddPhoneNumber`. `UpdateUser` is still a
`FindOneAndReplace` and sixteen other endpoints still call it with a document read earlier in
the same request, so the atomically written counter is still erased by whoever replaces the
document next.

## Running them

Requires `docker` or `podman` (running) and a Go toolchain.

```sh
./run-tests.sh
```

It starts a throwaway MongoDB on port 27019 (N-2's runner uses 27018, so both can run at once),
runs the tests, then removes the container. It deliberately runs the N-2 suite as well: the
AddPhoneNumber-only tests passing while these fail is the whole point.

## Where the test file lives

`pkg/grpc/service/account_management_ratelimit_crossendpoint_test.go`.

Same constraint as N-2: it is `package service` and uses unexported identifiers. It also reuses
`countingWhatsAppClient`, `newSendBarrier` and `addRateLimitTestUser` from
`account_management_ratelimit_concurrency_test.go`, so that file must be present.

## The tests

| Test | What it forces |
| --- | --- |
| `TestPhoneRateLimitSurvivesConcurrentLogin` | The budget is spent while `LoginWithEmail` is inside argon2id; the login's replace restores the counter |
| `TestPhoneRateLimitSurvivesConcurrentTokenRefresh` | Same, against `RenewJWT`, whose window is only a few DB round trips |
| `TestPhoneRateLimitIsNotRestoredByRepeatedLogins` | Five rounds of the above, to show the reset is repeatable and the budget is unbounded |
| `TestPhoneVerificationAttemptsArePruned` | Expired attempts must be removed by the reservation, not just ignored (M-12) |
| `TestPhoneVerificationSlotsAreNotBurnedWithoutASend` | A slot may only stay consumed when a Meta call was actually made |

Nothing is injected into production code. The windows used are the ones production already has:
argon2id password verification, and the renew-token round trips.
