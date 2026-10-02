# Inspect events and recover delivery

The service Events view describes declared events, consumers and dead-letter work. Event operations require NATS; rule execution additionally requires Temporal.

## Follow an event

Start with its service-qualified name, such as `hello.Greeted`. Inspect its schema, subscribers and rule definitions. Publish a test event only when you intend to trigger real consumers. The API audits that command, but does not copy the event payload into control audit.

In the demo, a valid greeting reaches hello's reactor, formatter's reactor and the formatter rule. The rule excludes `skip`; the independent reactors do not. This difference is expected, not lost delivery.

## Diagnose dead letters

1. Open the affected subscriber/consumer and inspect the failed delivery.
2. Read the error and attempt information. Determine whether the payload is invalid, the consumer is unavailable or the handler has a bug.
3. Correct the service or data path before redrive.
4. Redrive only the intended items/target and inspect the per-item result.
5. Verify the business effect and retained idempotency identity.

Batch recovery can partially succeed. Do not repeat the whole batch blindly: successful items may already have executed. Purge discards dead-letter records and requires an explicit destructive action.

## Delivery guarantees

Publisher deduplication and reactor idempotency solve different problems. Retrying a publish with the same event ID can avoid another stored event within the broker's policy; a consumer can still receive the same stored event more than once. Persist business deduplication with the business mutation where needed.

The example's counters and deduplication sets are process-local and bounded. They demonstrate behavior, not production persistence.

See [EventService](../reference/services/event-service.md) for methods/errors and [SDK events](../sdk/events.md) for author options.
