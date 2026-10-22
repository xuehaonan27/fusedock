# Contributing

Provider additions are the main extension point. Read [the provider guide](docs/provider-guide.md) and include fixtures that exercise pagination, unusual names, context cancellation, and byte ranges without real credentials.

Before opening a change:

```sh
make check
```

Keep provider-specific paths, authentication, and cloud object types inside the provider package. Changes to `pkg/provider` are contract changes and need tests that show how existing providers remain correct.

Do not commit passwords, OAuth tokens, cookies, signed URLs, or account fixtures derived from a real user. Native mount changes should state which operating system and filesystem driver were exercised.

FuseDock is not affiliated with Peking University or any other storage provider. Provider names are used only to identify interoperability targets.
