# Wiring: bindings and rules as YAML + CEL, with a graph editor — design

Date: 2026-09-30. Status: agreed direction, implementation in progress.

## Intent

Stated by the user:

- the wiring Backplane configures between services (bindings and rules) needs
  a graphical editor, "gamified" like the system map, and a YAML editor with
  highlighting and checks while typing;
- it lives in a new top-level section, **Wiring**;
- the custom text DSL is removed; YAML with CEL expressions is the format;
- the API and storage may be changed freely (no versioned `v2` names).

Assumed: bindings stay declarative (no loops or branches beyond `when`); logic
that needs a language belongs in a service's workflow or activity, called as a
step. Runtime pieces that the review found sound stay: typed CEL checking
against manifest schemas, deterministic evaluation, the executor workflow and
saga, the rules engine, append-only versions with audit.

## Why YAML + CEL

Structure is data, logic inside fields is CEL — the model of Google Cloud
Workflows, Kubernetes admission policies and GitHub Actions expressions. It maps
1:1 onto a graph (step = node, CEL reference = edge, schema field = port), is
typed against manifest schemas while editing, round-trips losslessly, and keeps
determinism for Temporal. A general language (Starlark) would make the graph
one-way and move type errors to run time.

## Model (proto, changed in place)

```proto
message BindingDefinition {
  string hook = 1;                     // "<service>.<Hook>"
  map<string, Step> steps = 2;         // key = step name = CEL variable
  google.protobuf.Value result = 3;    // JSON tree, see Values
  string description = 4;
  EditorLayout editor = 15;            // ignored by compile and execution
}
message RuleDefinition {
  string event = 1;                    // "<service>.<Event>"
  string when = 2;                     // CEL bool over event, meta
  map<string, Step> steps = 3;
  string description = 4;
  EditorLayout editor = 15;
}
message Step {
  string activity = 1;                 // "<service>.<Activity>"
  google.protobuf.Value input = 2;
  string when = 3;
  repeated string after = 4;
  string undo = 5;
  google.protobuf.Value undo_input = 6;
  Retry retry = 7;
  google.protobuf.Duration start_to_close = 8;
  google.protobuf.Duration heartbeat = 9;
  string description = 10;
}
message EditorLayout { map<string, Point> nodes = 1; repeated Note notes = 2; }
```

**Values** are `google.protobuf.Value`, i.e. plain JSON: a string is a CEL
expression, an object is an object built field by field (recursively), a list is
a list, numbers/booleans/null are literals. A literal string is a CEL string
literal (`"'hello'"`). Absent means `{}` (input) or `{}` (result). This makes
protojson the canonical JSON and YAML a direct rendering of it:

```yaml
hook: hello.Greet
description: Greets through the formatter
steps:
  formatted:
    activity: formatter.Format
    input: { name: req.name }
result: { text: formatted.text }
editor: { nodes: { formatted: { x: 240, y: 80 } } }
```

Step order is not significant: execution follows dependencies; ties break by
step name. The shared messages move to a common `wiring.proto` (`Step`,
`Violation`, `EditorLayout`); `BindingStep`, `BindingValue`, `BindingField`,
`ParseBinding`, `FormatBinding`, `ParseRule`, `FormatRule` and the DSL
(`internal/bindings/dsl.go`) are removed. The Go model becomes a thin view over
the proto so no field is lost on save. A migration rewrites stored versions to
the new JSON shape.

## Validation with positions

`Violation { string path; string code; string message; Range expr; }`:

- `path` is a JSON Pointer by name, e.g. `/steps/send/input/subject`, stable
  under reordering and unambiguous for any key;
- `expr` is the byte range inside the CEL expression (from CEL issue
  locations), empty when the problem is the field itself.

The API stays proto/JSON. YAML lives at the edges (console, later a CLI): the
console parses YAML with source ranges, maps `path` to the node and adds the
`expr` offset for an exact squiggle. Validation runs debounced on every edit.
Save, Rollback, Delete and Pause take `base_version`; a mismatch is `ABORTED`
(two editors never silently overwrite each other).

## Analysis and catalog for the editor

- `GetWiringCatalog` — per service (latest manifests): hooks (input/output
  schema), activities (input/output schema, kind, defaults), events (schema).
  The editor palette and port types come from here.
- `AnalyzeBinding` / `AnalyzeRule` — for a definition (saved or draft): per step
  its level, dependencies split into data/`after`/`when`, the CEL type of every
  value node, and port-level references (`/steps/send/input/subject ←
  render.subject`). Graph edges and hover types come from here.
- `RenameStep` — rewrites the name in every CEL expression (AST-based).

## Reliability fixes

- **Broken state:** when manifests change, every current binding and rule is
  re-validated; List/Watch/Get report `BROKEN` with violations instead of
  `BOUND`/active, and a broken rule is visible (not only a log line).
- **Execution spec** is its own tagged struct with `spec_version`; unknown
  versions fail loudly instead of mis-decoding in-flight runs.
- **Scheduling by dependencies:** a step starts as soon as its dependencies
  finish, not when its whole level does.
- **Rule runs** get the same step timeline as binding runs.
- **CEL cost** is estimated at save time.

## Console: Wiring

Route `/wiring` (sidebar: Services, Wiring, Explore, Audit).

- Left: bindings grouped by hook owner, rules grouped by event owner, state
  badges (bound, unbound, required-unbound, broken, paused), search, *New
  binding*, *New rule*; the palette of activities (from the catalog) below.
- Center: **Graph | YAML** on the same draft, switching without loss.
  - Graph (xyflow): a trigger node (hook input or event payload + meta), step
    nodes (activity, typed input ports per schema field with expression chips,
    output ports), a result node (hook output schema). Edges from analysis;
    `after` edges drawn dashed between node headers. Drag an activity from the
    palette to add a step; drag an output port to an input port to write the
    reference; click a port to edit its CEL inline with completion; node menu
    for `when`, retry, timeouts, undo, rename, delete. Layout is saved in
    `editor`; missing positions are laid out by levels. Service colors as on
    the map. After a test run the graph overlays each step's status and time.
  - YAML (CodeMirror YAML mode): syntax errors immediately, validation
    squiggles at exact positions, completion for keys, activity names, and CEL
    variables/fields inside expression strings.
- Right: inspector of the selected node, problems list (click → node / line),
  and the version bar (current version, draft state, Test, Save with comment,
  Versions with diff and rollback, Runs).
- The Services map, the Automation tab and run views link into Wiring.

## Stages

1. Backend: model and `wiring.proto`, migration, compile on the new model,
   pointer paths and ranges, `base_version`, catalog, analysis, rename, broken
   state, spec version, dependency scheduling, rule timeline, cost estimate;
   DSL removed; demo, conformance and tests moved to the new format.
2. Console: Wiring section with lists, YAML editor with live validation and
   completion, versions, tests and runs; Automation tab reduced to a summary
   that links into Wiring.
3. Console: graph editor with two-way sync, palette, typed ports, inline CEL,
   run overlay.

Each stage ends with `make web-check`, Go tests and lint, `make test-dev`, and a
browser check in both themes.
