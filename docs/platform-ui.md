# Platform compositions

`@gopherex/backplane-platform-ui` connects the generic kit to the generated console
APIs. It consumes `BackplaneProvider`; it does not create another connection,
router or console shell. English resources are exported as `platformEnglish` for
`backplane.platform`. Components accept the host's dark/light mode.

| Export | Current behavior |
| --- | --- |
| `ServiceCatalog`, `ServiceInspector` | Snapshot catalog watch; instances, readiness reasons, node tree, effective config and source metadata |
| `ConfigurationPanel` | Snapshot config watch; explicit Live-path overrides, authoritative validation/save, revision pages/diff/rollback, per-instance application status |
| `OperationSelector`, `ServiceOperations` | Declared hook/activity/event/workflow selection; schemapb input form when declared, raw JSON otherwise; exact request/result display |
| `BindingEditor`, `RuleEditor` | Generated definition JSON editor, server validation, version save and draft diff |
| `WorkflowRuns`, `RunInspector` | Filtered/pageable runs, exact history IDs, payloads/failures, explicit cancel/terminate/signal commands |
| `SchedulesPanel` | Declared/current schedule state, explicit pause/resume/trigger; schedule definitions remain manifest-owned |
| `EventStreams`, `DeadLettersPanel` | Event counts/subscribers, message pages, dead-letter details and selected-sequence redrive/purge |
| `AuditFeed`, `useAuditFeed` | Filters, bounded deduplicated replay, pagination and cursor-expiry state |
| `ExplorePanel`, `ObsResults`, `TraceLookup` | Capabilities, native source/field discovery, signal/language/query, logs/metrics/traces, complete raw responses and trace correlation |

These are reusable controls and technical catalog fixtures, not the agreed product
console layout. Binding/rule definitions and workflow command envelopes currently
use generated-contract JSON editors. Configuration overrides edit exact JSON text
per Live path; the server validates them against each instance. A configuration
schema does not grant permission to write non-Live fields. Effective masked
instance configuration is never copied into an editable override draft.

`usePlatformQuery` cancels old requests on key, provider, session or connection
replacement and prevents data from an old session appearing during the render
before cleanup. Read failures retain same-session data for an explicit retry.
`usePlatformAction` suppresses duplicate in-flight submission and never retries
mutations. A failure preserves the draft and reports that the outcome must be
checked before repeating the action. Read cancellation and a canceled browser
mutation do not imply rollback of work already accepted by the server.

Audit saves every returned cursor, including empty batches. Reconnect resumes
strictly after that cursor and deduplicates by entry ID. Sequence comparison uses
bigint. An expired cursor presents a visible gap and an explicit reload, rather
than silently claiming uninterrupted history. Views retain 1,000 entries by
default (configurable up to 10,000) and report omitted old entries. No deployment
retention settings appear in the UI.

Explore offers only languages advertised by capabilities. The field-suggestion
filter affects discovery, not the submitted expression; native queries are never
silently rewritten. Source discovery includes undeclared sources. Query bounds
use exact nanosecond strings. Original metric timestamp/value strings remain in
the raw API response; plotting explicitly converts timestamps to milliseconds.
Tempo JSON uses `lossless-json`, including extension fields. OTLP base64 span and
trace IDs become hex for correlation; unknown JSON stays available in the raw
viewer. A typed view is not a promise to reconstruct flattened OTLP types.

The catalog's `PlatformFixture` is an explicit local transport. It exercises the
same generated messages and UI APIs, including storage failure, no mutation retry,
cursor advancement/expiry, session replacement, schema input and integers above
2^53. Real installation acceptance remains a separate dev-stack check.

The live dev installation validates service relay, config save, a seeded binding,
hook execution, audit and stored logs/metrics through these components. See
[development](development.md). The composed catalog workflow additionally links
search results to stack/source, logs, trace and back without an error engine.
