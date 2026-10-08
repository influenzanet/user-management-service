# Normalize phone numbers

Brings the phone numbers already stored to the single form the service now writes and compares:
E.164 (`+` followed by the digits, no separators, no national trunk zero), for example
`+44 (0)20 7946 0018` becomes `+442079460018` and `+39 06 1234 5678` becomes `+390612345678`.

It looks at the phone contacts of every account and at the number a pending verification code is
bound to (`account.phoneVerificationCode.phone`). With `-apply` it also seeds the claim collection
the service uses to keep a verified number on one account (see "Claims").

## Configuration

The database connection comes from the `USER_DB_*` environment variables (read through
`config.GetUserDBConfig`, exactly as the service does), together with the other `DB_*` settings.
Copy `example.env` as `.env` and edit it with the desired values. The tool connects to whatever
database those variables point to, in the shell it is started from: before running it, and
above all before running it with `-apply`, check which database the environment points to (print
`USER_DB_CONNECTION_STR` and make sure it is the one of the intended environment).

## Usage

Flags:
- `-instance`: instance ID to process; several instances can be given separated by commas
- `-apply`: write the changes. Without it nothing is written
- `-dry-run`: the default, accepted for clarity; it cannot be combined with `-apply`. Writing
  needs `-apply`: `-dry-run=false` on its own is refused with an error instead of writing.

Report what would change on the instance INSTANCE_ID (nothing is written):

```
go run ./tools/normalize-phone-numbers -instance=INSTANCE_ID
```

Apply the changes:

```
go run ./tools/normalize-phone-numbers -instance=INSTANCE_ID -apply
```

Run it once as a dry run first and read the report.

## What the report says

For each instance:
- accounts with a phone number, and how many numbers are already normalised;
- the numbers that would change (or changed), masked, e.g. `+44***0018 -> +44***0018`, with the
  number of digits before and after (`digits 13 -> 12`) and a `(trunk zero removed)` hint when a
  national trunk zero was dropped. A masked number keeps only its first three characters and its
  last four, so a trunk zero fix looks identical before and after; the digit counts are what shows
  the change without unmasking anything;
- the numbers that cannot be turned into a valid E.164 number: they are reported and left untouched;
- the collisions: numbers that at least two different accounts hold VERIFIED once normalised. The
  service allows one verified holder per number, so these cannot be settled by the tool. They are
  reported and never merged, and none of the accounts involved is modified, even with `-apply`.
  They have to be settled by hand (for instance by removing the number from the account that does
  not own it) before running the tool again;
- the notes: a number carried by several accounts with exactly one of them holding it verified.
  All of them are normalised; the line only tells that the other accounts carry a number somebody
  else has proved;
- the claims (see below).

A number shared by accounts that hold it all unverified is legitimate (an unverified number proves
nothing) and is normalised like any other, without a line in the report.

## Claims

The service keeps one document per verified number in the `verifiedPhones` collection of the
instance (`_id` = the E.164 number, plus the owner account), and consults it when somebody proves a
number. With `-apply`, the tool inserts that document for every account that holds a number
verified, outside the collisions: absent claims are inserted, a claim already in the holder's name
is left, and a claim in the name of another account is reported (`claim conflict`) and left as it
is. It is idempotent. A dry run reports how many claims would be seeded.

## Guarantees

- Writes are targeted `$set` updates whose filter names the number that was read, so a number
  changed by a participant in the meantime is not overwritten; such accounts are counted as
  skipped and picked up by the next run.
- When an account's contact and its verification code carry the same number, both change in a
  single update.
- It is idempotent: running it again changes nothing.
- Nothing is deleted and no account is merged.

## When to run it

Run the tool on each instance when the version that normalises numbers is deployed: it is part of
the deploy of this version, not an optional clean-up. Until it has run, the check that a number is
already taken compares against the stored value as it is, so a verified number stored in a legacy
non-E.164 spelling, for example with the trunk zero (`+4402079460018`), is not recognised as the
same telephone as `+442079460018`. Until the claims are seeded, a number verified before this
version is not protected by the claim collection either.
