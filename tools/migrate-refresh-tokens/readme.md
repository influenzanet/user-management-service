# Migrate Refresh tokens

## Configuration

Database configuration is expected in environment variables exactly the same way as the service itself

Copy the env.example as '.env' and edit it with the desired values

## Usage

Flags:
-instance: name of the instance to migrate 
-min: minimal date expected for a token, if below this date the token is considered as invalid
-commit: will actually do the change to the db, it's advised to run the command without this flag once before

Run a migration test for all users for the instance INSTANCE_ID

```
./run.sh --instance=INSTANCE_ID -min=2022-11-01
```

Run and apply migrations
```
./run.sh --instance=INSTANCE_ID -min=2022-11-01 -commit
```

If -commit flag is not set, no change is applied on the db (default behavior to avoid errors)