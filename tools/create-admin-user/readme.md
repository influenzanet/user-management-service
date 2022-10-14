## Usage

Environment variables for database config must be present. To set them, you can use something like in the `run-example.sh` script.

The CLI application accepts the following arguments:

- instance: to define in which instance the admin user should be created in
- language: to define the default language of the admin user.
- use2FA: boolean flag to define if auth type should be "2FA".

```sh
./run.sh --instance <INSTANCE_ID> --language <preferredLanguage>
```

or:

```sh
./run.sh --instance <INSTANCE_ID> --language <preferredLanguage> --use2FA
```
