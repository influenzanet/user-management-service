# Weekday Assignation tool

This tool can be used to test or reassign weekday (weekday to send auto-messages) for users.

## Configuration

Database configuration and weekday strategy are expected in environment variables exactly the same way as the service itself

Copy the env.example as '.env' and edit it with the desired values

## Usage


Run a test assignation for all users for the instance INSTANCE_ID

```
./run.sh --instance=INSTANCE_ID
```

Run and apply assignation to all users
```
./run.sh --instance=INSTANCE_ID --commit
```

If --commit flag is not set, no change is applied on the db (default behavior to avoid errors)