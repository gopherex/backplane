# Typed and live configuration

Embed `config.Backplane` in your application configuration and give domain fields JSON names. Those names become stable configuration paths.

```go
type GreeterConfig struct {
    Salute string `json:"salute" schemapb:"default=Hello;description=Word before the name"`
    Suffix config.Live[string] `json:"suffix" schemapb:"default=!;description=Greeting suffix"`
    Excited config.Live[bool] `json:"excited" schemapb:"default=false"`
}

type Config struct {
    config.Backplane `json:"backplane"`
    Greeter GreeterConfig `json:"greeter"`
}
```

Normal fields are startup configuration. `config.Live[T]` marks fields that can change without restarting. Read them with `Get()` at the point of use; copying a live value into an ordinary field would freeze it.

```go
suffix := cfg.Greeter.Suffix.Get()
cfg.Greeter.Suffix.Watch(func(value string) {
    // React to an accepted value; keep callbacks short.
})
```

## Sources and precedence

The service combines file configuration, environment overrides and the distributed live configuration source. Environment variables override file values. The Consul source is filtered to declared live paths, so an operator cannot use live configuration to rewrite a startup-only listener or credential. See [exact configuration names](../reference/configuration.md).

Schemas use the shared schemapb runtime for defaults, constraints, CEL validation and masking. Keep schema annotations and application validators consistent. Configuration schema construction errors should be fixed before deploying the service.

## Domain validation

Implement `Validate() error` for constraints beyond field annotations. The hello example rejects an empty salute and a suffix longer than eight characters or containing a newline. Validation runs at startup and on candidate live updates; a rejected live candidate must not become the active value.

A server can save a schema-valid revision that an instance rejects with its custom validator. The console therefore exposes per-instance desired/applied/rejected state. Application acceptance is a separate step from database persistence.

## Secrets

Use secret annotations and deployment secret injection. Do not copy a masked value back as if it were the secret. Keep startup-only secrets outside the operator's live-edit surface. Custom validation errors and watch logs must not disclose values merely because the schema masks them elsewhere.

For the operator workflow, optimistic revisions and rollback, see [change configuration](../guides/configuration.md).
