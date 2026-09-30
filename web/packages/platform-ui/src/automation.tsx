import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { Badge, Button, ConfirmAction, Count, DetailDrawer, EmptyState, Input, KeyValueList, Panel, StatusBadge, StatusDot, Switch, TabBar, Timestamp, type StatusTone } from '@gopherex/backplane-ui';
import { CodeEditor, DiffViewer, JSONViewer } from '@gopherex/backplane-editors';
import { ArrowDown, Cable, CircleCheck, CircleX, FlaskConical, GitBranch, History, Pause, Pencil, Play, Plus, RotateCcw, Trash2, Undo2, Zap } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, durationText, enumLabel } from './format.js';
import { RunDrawer, RunStatusFilter, RunTable } from './runs.js';

type Mode = 'dark' | 'light';
type Violation = { path: string; message: string };

const bindingTone: Record<api.BindingState, StatusTone> = {
  [api.BindingState.UNSPECIFIED]: 'neutral', [api.BindingState.BOUND]: 'success', [api.BindingState.UNBOUND]: 'neutral', [api.BindingState.REQUIRED_UNBOUND]: 'danger',
};
const stepTone: Record<api.StepRunStatus, StatusTone> = {
  [api.StepRunStatus.UNSPECIFIED]: 'neutral', [api.StepRunStatus.NOT_RUN]: 'neutral', [api.StepRunStatus.SCHEDULED]: 'info', [api.StepRunStatus.STARTED]: 'info',
  [api.StepRunStatus.COMPLETED]: 'success', [api.StepRunStatus.FAILED]: 'danger', [api.StepRunStatus.TIMED_OUT]: 'danger', [api.StepRunStatus.CANCELED]: 'warning',
};

/** Hook bindings and event rules of one service. */
export function AutomationPanel({ service, mode }: { service: string; mode: Mode }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  // Live snapshots: another operator's save shows up without a reload.
  const hookWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => bindings.watchBindings(create(api.WatchBindingsRequestSchema, { service }), { signal }), [bindings, service]));
  const ruleWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => rules.watchRules(create(api.WatchRulesRequestSchema), { signal }), [rules]));
  const hooks = { value: hookWatch.value, loading: hookWatch.status === 'loading' }, allRules = { value: ruleWatch.value, loading: ruleWatch.status === 'loading' };
  const own = allRules.value?.rules.filter((rule) => rule.current?.definition?.event.startsWith(`${service}.`)) ?? [];
  const [selection, setSelection] = useState<{ kind: 'binding'; hook: string } | { kind: 'rule'; id: string } | { kind: 'new-rule' }>();
  const current = selection ?? (hooks.value?.bindings[0] ? { kind: 'binding' as const, hook: hooks.value.bindings[0].hook } : own[0] ? { kind: 'rule' as const, id: own[0].id } : undefined);
  const refresh = () => undefined;
  const itemClass = 'flex w-full items-center gap-2 border-b border-border px-3 py-2 text-left last:border-b-0 hover:bg-raised aria-pressed:bg-primary/10';
  return <div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[320px_minmax(0,1fr)]">
    <div className="flex min-h-0 min-w-0 flex-col gap-4">
      <Panel fill title={<><Cable className="size-4 text-muted-foreground" />{text('bindings')}</>} count={hooks.value?.bindings.length} flush description={text('bindingsHelp')}>
        {!hooks.value?.bindings.length && <EmptyState className="py-6" title={hooks.loading ? text('loading') : text('noHooks')} />}
        {hooks.value?.bindings.map((binding) => <button key={binding.hook} type="button" className={itemClass} aria-pressed={current?.kind === 'binding' && current.hook === binding.hook} onClick={() => setSelection({ kind: 'binding', hook: binding.hook })}>
          <span className="min-w-0 flex-1"><span className="flex items-center gap-1.5"><span className="truncate font-mono text-xs">{binding.hook.slice(service.length + 1) || binding.hook}</span>{binding.required && <Badge variant="outline">{text('required')}</Badge>}</span>
            <span className="block truncate text-2xs text-muted-foreground">{binding.current ? `v${binding.current.version} · ${text('steps', { count: binding.current.definition?.steps.length ?? 0 })}` : text('noBinding')}</span></span>
          <StatusBadge tone={bindingTone[binding.state]}>{enumLabel(api.BindingState, binding.state)}</StatusBadge>
        </button>)}
      </Panel>
      <Panel fill title={<><Zap className="size-4 text-muted-foreground" />{text('rules')}</>} count={own.length} flush description={text('rulesHelp')}
        actions={<Button size="xs" variant="outline" onClick={() => setSelection({ kind: 'new-rule' })}><Plus />{text('newRule')}</Button>}>
        {!own.length && <EmptyState className="py-6" title={allRules.loading ? text('loading') : text('noRules')} />}
        {own.map((rule) => <button key={rule.id} type="button" className={itemClass} aria-pressed={current?.kind === 'rule' && current.id === rule.id} onClick={() => setSelection({ kind: 'rule', id: rule.id })}>
          <StatusDot tone={rule.paused ? 'warning' : 'success'} />
          <span className="min-w-0 flex-1"><span className="block truncate text-sm">{rule.current?.name || rule.id}</span>
            <span className="block truncate font-mono text-2xs text-muted-foreground">{rule.current?.definition?.event} · v{rule.current?.version.toString()}</span></span>
          {rule.paused && <StatusBadge tone="warning">{text('paused')}</StatusBadge>}
        </button>)}
      </Panel>
    </div>
    <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      {!current && <Panel><EmptyState icon={<GitBranch />} title={text('noAutomation')} description={text('noAutomationHelp')} /></Panel>}
      {current?.kind === 'binding' && <BindingDetail key={current.hook} hook={current.hook} info={hooks.value?.bindings.find((entry) => entry.hook === current.hook)} mode={mode} onChanged={refresh} />}
      {current?.kind === 'rule' && <RuleDetail key={current.id} id={current.id} mode={mode} onChanged={refresh} onDeleted={() => { setSelection(undefined); refresh(); }} />}
      {current?.kind === 'new-rule' && <RuleEditorPanel key="new" service={service} mode={mode} onSaved={(id) => { setSelection({ kind: 'rule', id }); refresh(); }} onCancel={() => setSelection(undefined)} />}
    </div>
  </div>;
}

/* ---------- Program view: req → steps → result ---------- */

function valueText(value?: api.BindingValue): string | undefined {
  if (!value) return undefined;
  if (value.expr) return value.expr;
  if (value.fields.length) return `{ ${value.fields.map((field) => `${field.name}: ${field.expr}`).join(', ')} }`;
  return undefined;
}
function Pipeline({ source, steps, result, when }: { source: ReactNode; steps: api.BindingStep[]; result?: api.BindingValue; when?: string }) {
  const text = usePlatformText();
  const node = (children: ReactNode, tone = 'border-border') => <div className={`rounded-md border bg-background px-3 py-2 ${tone}`}>{children}</div>;
  const arrow = <div className="flex justify-center py-0.5 text-subtle"><ArrowDown className="size-3.5" /></div>;
  return <div className="grid">
    {node(<div className="flex items-center gap-2 text-xs"><Badge variant="secondary">{text('trigger_label')}</Badge><span className="font-mono">{source}</span>{when && <span className="ml-auto truncate font-mono text-muted-foreground" title={when}>when {when}</span>}</div>)}
    {steps.map((step) => <div key={step.name}>{arrow}{node(<div className="grid gap-1 text-xs">
      <div className="flex flex-wrap items-center gap-2"><span className="font-mono font-medium">{step.name}</span><span className="text-muted-foreground">→</span><span className="font-mono text-link">{step.activity}</span>
        {step.after.length > 0 && <Badge variant="outline">{text('after', { steps: step.after.join(', ') })}</Badge>}
        {step.retry?.attempts ? <Badge variant="outline">{text('retries', { count: step.retry.attempts })}</Badge> : null}
        {step.startToClose && <Badge variant="outline">{durationText(step.startToClose)}</Badge>}</div>
      {step.when && <div className="truncate font-mono text-muted-foreground" title={step.when}>when {step.when}</div>}
      {valueText(step.input) && <div className="truncate font-mono text-muted-foreground" title={valueText(step.input)}>in {valueText(step.input)}</div>}
      {step.undo && <div className="font-mono text-warning">undo → {step.undo}</div>}
    </div>, 'border-primary/30')}</div>)}
    {result !== undefined && <>{arrow}{node(<div className="flex items-center gap-2 text-xs"><Badge variant="secondary">{text('resultValue')}</Badge><span className="truncate font-mono text-muted-foreground">{valueText(result) ?? '{}'}</span></div>)}</>}
  </div>;
}

/* ---------- Bindings ---------- */

function BindingDetail({ hook, info, mode, onChanged }: { hook: string; info?: api.HookBinding; mode: Mode; onChanged: () => void }) {
  const client = useClient(api.BindingServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const [view, setView] = useState<'program' | 'source' | 'versions' | 'test' | 'runs'>('program'), [editing, setEditing] = useState(false), [epoch, setEpoch] = useState(0);
  const state = usePlatformQuery(`binding:${hook}:${epoch}`, (signal) => client.getBinding(create(api.GetBindingRequestSchema, { hook }), { signal }).catch(() => create(api.GetBindingResponseSchema)));
  const version = state.value?.version, definition = version?.definition;
  const changed = () => { setEpoch((value) => value + 1); onChanged(); };
  return <>
    <Panel title={<span className="font-mono">{hook}</span>} description={info?.description}
      actions={<>
        {info && <StatusBadge tone={bindingTone[info.state]}>{enumLabel(api.BindingState, info.state)}</StatusBadge>}
        <Button size="sm" variant="outline" onClick={() => setEditing(true)}><Pencil />{definition ? text('edit') : text('createBinding')}</Button>
        {definition && <ConfirmAction trigger={<><Trash2 className="size-3.5" />{text('delete')}</>} title={text('deleteBindingConfirm')} description={text('deleteBindingHelp')} disabled={action.pending || action.disabled}
          onConfirm={async (signal) => { const result = await action.run((abort) => client.deleteBinding(create(api.DeleteBindingRequestSchema, { hook }), { signal: AbortSignal.any([signal, abort]) })); if (result === undefined) throw new Error('Delete outcome is unavailable'); changed(); }} />}
      </>}>
      {version ? <KeyValueList items={[
        { label: text('version'), value: `v${version.version}${version.rollbackOf ? ` · ${text('rollbackOf', { revision: version.rollbackOf.toString() })}` : ''}` },
        { label: text('actor'), value: version.author, mono: true }, { label: text('time'), value: <Timestamp value={date(version.createdAt)} /> },
        { label: text('comment'), value: version.comment || '—' },
      ]} /> : <p className="m-0 text-sm text-muted-foreground">{info?.required ? text('requiredUnboundHelp') : text('unboundHelp')}</p>}
    </Panel>
    <TabBar aria-label={hook}>{(['program', 'source', 'versions', 'test', 'runs'] as const).map((name) =>
      <button key={name} type="button" aria-current={view === name ? 'page' : undefined} onClick={() => setView(name)} className="relative -mb-px inline-flex h-9 items-center border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground">{text(`tab_${name}`)}</button>)}</TabBar>
    {view === 'program' && <Panel>{definition ? <Pipeline source={hook} steps={definition.steps} result={definition.result} /> : <EmptyState className="py-6" title={text('noBinding')} />}</Panel>}
    {view === 'source' && <Panel flush>{definition ? <FormattedSource definition={definition} kind="binding" mode={mode} /> : <EmptyState className="py-6" title={text('noBinding')} />}</Panel>}
    {view === 'versions' && <BindingVersions hook={hook} current={version?.version} mode={mode} onChanged={changed} />}
    {view === 'test' && <BindingTest hook={hook} mode={mode} />}
    {view === 'runs' && <BindingRuns hook={hook} mode={mode} />}
    <DetailDrawer open={editing} onOpenChange={setEditing} size="xl" title={text('editBinding', { hook })} description={text('dslHelp')}>
      {editing && <SourceEditor kind="binding" initial={definition ?? create(api.BindingDefinitionSchema, { hook })} mode={mode} onSaved={() => { setEditing(false); changed(); }} />}
    </DetailDrawer>
  </>;
}

function FormattedSource({ definition, kind, mode }: { definition: api.BindingDefinition | api.RuleDefinition; kind: 'binding' | 'rule'; mode: Mode }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient);
  const key = JSON.stringify(definition, (_key, value) => typeof value === 'bigint' ? value.toString() : value);
  const state = usePlatformQuery(`format:${kind}:${key}`, async (signal): Promise<{ text: string }> => kind === 'binding'
    ? bindings.formatBinding(create(api.FormatBindingRequestSchema, { definition: definition as api.BindingDefinition }), { signal })
    : rules.formatRule(create(api.FormatRuleRequestSchema, { definition: definition as api.RuleDefinition }), { signal }));
  return <CodeEditor label={kind} value={state.value?.text ?? ''} language="text" mode={mode} readOnly height={320} />;
}

/** Character offset of a 1-based line and column. */
function offset(source: string, line: number, column: number): number {
  const lines = source.split('\n'), before = lines.slice(0, Math.max(0, line - 1)).reduce((sum, entry) => sum + entry.length + 1, 0);
  return Math.min(source.length, before + Math.max(0, column - 1));
}

/** DSL editor: live parse, validate against today's manifests, save with a comment. */
function SourceEditor({ kind, initial, ruleId, ruleName, mode, onSaved }: {
  kind: 'binding' | 'rule'; initial: api.BindingDefinition | api.RuleDefinition; ruleId?: string; ruleName?: string; mode: Mode; onSaved: (id?: string) => void;
}) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const [source, setSource] = useState<string>(), [baseline, setBaseline] = useState(''), [comment, setComment] = useState(''), [name, setName] = useState(ruleName ?? '');
  const [parsed, setParsed] = useState<{ definition?: api.BindingDefinition | api.RuleDefinition; errors: api.BindingParseError[] }>(), [violations, setViolations] = useState<Violation[]>();
  useEffect(() => {
    const controller = new AbortController();
    void (kind === 'binding' ? bindings.formatBinding(create(api.FormatBindingRequestSchema, { definition: initial as api.BindingDefinition }), { signal: controller.signal })
      : rules.formatRule(create(api.FormatRuleRequestSchema, { definition: initial as api.RuleDefinition }), { signal: controller.signal }))
      .then((response) => { setSource(response.text); setBaseline(response.text); }, () => { if (!controller.signal.aborted) { setSource(''); setBaseline(''); } });
    return () => controller.abort();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps -- the draft owns the text after it loads.
  useEffect(() => {
    if (source === undefined) return;
    const controller = new AbortController(), timer = setTimeout(() => {
      void (kind === 'binding' ? bindings.parseBinding(create(api.ParseBindingRequestSchema, { text: source }), { signal: controller.signal })
        : rules.parseRule(create(api.ParseRuleRequestSchema, { text: source }), { signal: controller.signal }))
        .then((response) => setParsed({ definition: response.definition, errors: response.errors }), () => undefined);
    }, 300);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [source, kind, bindings, rules]);
  const definition = parsed?.definition, blocked = !!parsed?.errors.length || action.pending || action.disabled;
  // Validate and save read the text as it is now, never a parse the debounce has not caught up with.
  const current = async (signal: AbortSignal) => {
    const response = kind === 'binding' ? await bindings.parseBinding(create(api.ParseBindingRequestSchema, { text: source ?? '' }), { signal })
      : await rules.parseRule(create(api.ParseRuleRequestSchema, { text: source ?? '' }), { signal });
    setParsed({ definition: response.definition, errors: response.errors });
    return response.errors.length ? undefined : response.definition;
  };
  const validate = () => void action.run(async (signal) => {
    const fresh = await current(signal); if (!fresh) return undefined;
    const response = kind === 'binding' ? await bindings.validateBinding(create(api.ValidateBindingRequestSchema, { definition: fresh as api.BindingDefinition }), { signal })
      : await rules.validateRule(create(api.ValidateRuleRequestSchema, { definition: fresh as api.RuleDefinition }), { signal });
    setViolations(response.violations); return response;
  });
  const save = () => void action.run(async (signal) => {
    const fresh = await current(signal); if (!fresh) return undefined;
    if (kind === 'binding') {
      const response = await bindings.saveBinding(create(api.SaveBindingRequestSchema, { definition: fresh as api.BindingDefinition, comment }), { signal });
      setViolations(response.violations); if (response.version) onSaved(); return response;
    }
    const response = await rules.saveRule(create(api.SaveRuleRequestSchema, { id: ruleId ?? '', name, definition: fresh as api.RuleDefinition, comment }), { signal });
    setViolations(response.violations); if (response.version) onSaved(response.version.ruleId); return response;
  });
  if (source === undefined) return <p className="text-sm text-muted-foreground" role="status">{text('loading')}</p>;
  return <div className="grid gap-3">
    {kind === 'rule' && <Input aria-label={text('name')} placeholder={text('ruleName')} value={name} onChange={(event) => setName(event.target.value)} />}
    <CodeEditor label={text('definition')} value={source} onChange={(value) => { setSource(value); setViolations(undefined); }} language="text" mode={mode} height={380}
      diagnostics={parsed?.errors.map((error) => { const from = offset(source, error.line, error.column); return { from, to: Math.min(source.length, from + 1), severity: 'error' as const, message: error.message }; })} />
    {!!parsed?.errors.length && <ul className="m-0 grid gap-1 pl-0 text-xs text-destructive" role="alert">{parsed.errors.map((error, index) => <li key={index} className="list-none font-mono">{error.line}:{error.column} {error.message}</li>)}</ul>}
    {violations && (violations.length ? <ul className="m-0 grid gap-1 pl-0 text-xs text-destructive" role="alert">{violations.map((violation, index) => <li key={index} className="list-none"><span className="font-mono">{violation.path}</span> {violation.message}</li>)}</ul>
      : <p className="m-0 inline-flex items-center gap-1 text-xs text-success" role="status"><CircleCheck className="size-3.5" />{text('valid')}</p>)}
    {definition && <Panel title={text('preview')}><Pipeline source={'hook' in definition ? definition.hook : definition.event} when={'when' in definition ? definition.when : undefined} steps={definition.steps} result={'result' in definition ? definition.result : undefined} /></Panel>}
    <div className="flex flex-wrap items-center gap-2">
      <Input className="h-8 flex-1" aria-label={text('comment')} placeholder={text('commentPlaceholder')} value={comment} onChange={(event) => setComment(event.target.value)} />
      <Button size="sm" variant="ghost" disabled={source === baseline} onClick={() => setSource(baseline)}><Undo2 />{text('reset')}</Button>
      <Button size="sm" variant="outline" disabled={blocked} onClick={validate}>{text('validate')}</Button>
      <Button size="sm" disabled={blocked || (kind === 'rule' && !name.trim())} onClick={save}>{action.pending ? text('pending') : text('save')}</Button>
    </div>
    {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
  </div>;
}

function BindingVersions({ hook, current, mode, onChanged }: { hook: string; current?: bigint; mode: Mode; onChanged: () => void }) {
  const client = useClient(api.BindingServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const state = usePlatformQuery(`binding-versions:${hook}:${current}`, (signal) => client.listBindingVersions(create(api.ListBindingVersionsRequestSchema, { hook, pageSize: 100 }), { signal }));
  const [selected, setSelected] = useState<api.BindingVersion>();
  const currentDefinition = state.value?.versions.find((entry) => entry.version === current)?.definition;
  return <VersionList versions={(state.value?.versions ?? []).map((entry) => ({ version: entry.version, author: entry.author, comment: entry.comment, createdAt: entry.createdAt, deleted: entry.deleted, rollbackOf: entry.rollbackOf, raw: entry }))}
    current={current} loading={state.loading} onOpen={(entry) => setSelected(entry.raw)}>
    <DetailDrawer open={!!selected} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="xl" title={selected && `v${selected.version}`} description={selected?.comment}
      actions={selected && selected.version !== current && !selected.deleted && <ConfirmAction trigger={<><RotateCcw className="size-3.5" />{text('rollbackTo', { revision: selected.version.toString() })}</>} title={text('rollbackConfirm')} description={text('rollbackBindingHelp')} disabled={action.pending || action.disabled}
        onConfirm={async (signal) => { const result = await action.run((abort) => client.rollbackBinding(create(api.RollbackBindingRequestSchema, { hook, version: selected.version }), { signal: AbortSignal.any([signal, abort]) })); if (result === undefined) throw new Error('Rollback outcome is unavailable'); setSelected(undefined); onChanged(); }} />}>
      {selected && <DefinitionDiff kind="binding" before={selected.definition} after={currentDefinition} mode={mode} />}
    </DetailDrawer>
  </VersionList>;
}

type VersionEntry<T> = { version: bigint; author: string; comment: string; createdAt?: api.BindingVersion['createdAt']; deleted: boolean; rollbackOf: bigint; raw: T };
function VersionList<T>({ versions, current, loading, onOpen, children }: { versions: VersionEntry<T>[]; current?: bigint; loading: boolean; onOpen: (entry: VersionEntry<T>) => void; children?: ReactNode }) {
  const text = usePlatformText();
  return <Panel title={<><History className="size-4 text-muted-foreground" />{text('versions')}</>} count={versions.length} flush>
    {!versions.length && <EmptyState className="py-6" title={loading ? text('loading') : text('noVersions')} />}
    <ol className="m-0 list-none p-0">{versions.map((entry) => <li key={entry.version.toString()}><button type="button" onClick={() => onOpen(entry)} className="flex w-full items-start gap-2.5 border-b border-border px-3 py-2 text-left hover:bg-raised">
      <Count className="mt-0.5">v{entry.version.toString()}</Count>
      <span className="min-w-0 flex-1"><span className="block truncate text-sm">{entry.deleted ? <span className="text-destructive">{text('deleted')}</span> : entry.comment || <span className="text-muted-foreground italic">{text('noComment')}</span>}</span>
        <span className="block truncate text-xs text-muted-foreground">{entry.author} · <Timestamp value={date(entry.createdAt)} />{entry.rollbackOf > 0n && ` · ${text('rollbackOf', { revision: entry.rollbackOf.toString() })}`}</span></span>
      {entry.version === current && <StatusBadge tone="accent" dot={false}>{text('current')}</StatusBadge>}
    </button></li>)}</ol>
    {children}
  </Panel>;
}

function DefinitionDiff({ kind, before, after, mode }: { kind: 'binding' | 'rule'; before?: api.BindingDefinition | api.RuleDefinition; after?: api.BindingDefinition | api.RuleDefinition; mode: Mode }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const format = (definition: api.BindingDefinition | api.RuleDefinition | undefined, signal: AbortSignal) => !definition ? Promise.resolve('')
    : kind === 'binding' ? bindings.formatBinding(create(api.FormatBindingRequestSchema, { definition: definition as api.BindingDefinition }), { signal }).then((response) => response.text)
      : rules.formatRule(create(api.FormatRuleRequestSchema, { definition: definition as api.RuleDefinition }), { signal }).then((response) => response.text);
  const key = JSON.stringify([before, after], (_key, value) => typeof value === 'bigint' ? value.toString() : value);
  const state = usePlatformQuery(`diff:${kind}:${key}`, (signal) => Promise.all([format(before, signal), format(after, signal)]));
  return <div className="grid gap-2"><div className="text-xs text-muted-foreground">{text('diffAgainstCurrent')}</div>
    {state.value ? <DiffViewer label={text('versions')} before={state.value[0]} after={state.value[1]} language="text" mode={mode} height={420} /> : <p className="text-sm text-muted-foreground">{text('loading')}</p>}</div>;
}

function CallOutcome({ result, mode }: { result: api.CallResult; mode: Mode }) {
  const text = usePlatformText();
  return <div className="grid gap-2">
    <div className="flex flex-wrap items-center gap-2 text-sm">{result.error ? <StatusBadge tone="danger">{text('failed')}</StatusBadge> : <StatusBadge tone="success">{text('succeeded')}</StatusBadge>}
      {result.took && <span className="text-xs text-muted-foreground">{durationText(result.took)}</span>}
      {result.workflowId && <span className="font-mono text-2xs text-muted-foreground">{result.workflowId}</span>}</div>
    {result.error ? <pre className="m-0 rounded-md border border-destructive/30 bg-destructive/10 p-2 font-mono text-xs whitespace-pre-wrap text-destructive">{result.errorType && `${result.errorType}: `}{result.error}</pre>
      : <JSONViewer label={text('output')} value={result.output || 'null'} mode={mode} />}
  </div>;
}

function BindingTest({ hook, mode }: { hook: string; mode: Mode }) {
  const client = useClient(api.BindingServiceClient), text = usePlatformText(), action = usePlatformAction<api.TestBindingResponse>();
  const [input, setInput] = useState('{}');
  return <Panel title={<><FlaskConical className="size-4 text-muted-foreground" />{text('testBinding')}</>} description={text('testBindingHelp')}>
    <div className="grid gap-3">
      <CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} height={140} />
      <div><Button size="sm" disabled={action.pending || action.disabled} onClick={() => void action.run((signal) => client.testBinding(create(api.TestBindingRequestSchema, { hook, input }), { signal }))}><Play />{action.pending ? text('pending') : text('runTest')}</Button></div>
      {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
      {!!action.value?.violations.length && <ul className="m-0 grid gap-1 pl-0 text-xs text-destructive" role="alert">{action.value.violations.map((violation, index) => <li key={index} className="list-none"><span className="font-mono">{violation.path}</span> {violation.message}</li>)}</ul>}
      {action.value?.result && <CallOutcome result={action.value.result} mode={mode} />}
    </div>
  </Panel>;
}

function BindingRuns({ hook, mode }: { hook: string; mode: Mode }) {
  const client = useClient(api.BindingServiceClient), text = usePlatformText();
  const [status, setStatus] = useState(api.RunStatus.UNSPECIFIED), [tests, setTests] = useState(false), [selected, setSelected] = useState<api.Run>();
  const state = usePlatformQuery(`binding-runs:${hook}:${status}:${tests}`, (signal) => client.listBindingRuns(create(api.ListBindingRunsRequestSchema, { hook, status, tests, pageSize: 50 }), { signal }));
  return <Panel title={text('runs')} count={state.value?.runs.length} flush actions={<label className="flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={tests} onCheckedChange={setTests} />{text('testRuns')}</label>}>
    <div className="border-b border-border px-3 py-2"><RunStatusFilter value={status} onChange={setStatus} /></div>
    <RunTable runs={state.value?.runs ?? []} loading={state.loading} onOpen={setSelected} />
    <RunDrawer run={selected} onClose={() => setSelected(undefined)} mode={mode} source={{
      load: async (run, signal) => (await client.getBindingRun(create(api.GetBindingRunRequestSchema, run), { signal })).run ?? create(api.GetRunResponseSchema),
      cancel: (run, signal) => client.cancelBindingRun(create(api.CancelBindingRunRequestSchema, run), { signal }),
    }}>{selected && <BindingSteps workflowId={selected.workflowId} runId={selected.runId} />}</RunDrawer>
  </Panel>;
}

/** Step-by-step timeline of a binding run. */
function BindingSteps({ workflowId, runId }: { workflowId: string; runId: string }) {
  const client = useClient(api.BindingServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`binding-run:${workflowId}:${runId}`, (signal) => client.getBindingRun(create(api.GetBindingRunRequestSchema, { workflowId, runId }), { signal }));
  if (!state.value?.steps.length) return null;
  return <Panel title={text('steps', { count: state.value.steps.length })} flush>
    {state.value.steps.map((step, index) => { const start = date(step.startedTime), end = date(step.closeTime); return <div key={index} className="grid gap-0.5 border-b border-border px-3 py-2 text-xs last:border-b-0">
      <div className="flex flex-wrap items-center gap-2"><StatusBadge tone={stepTone[step.status]}>{enumLabel(api.StepRunStatus, step.status)}</StatusBadge>
        <span className="font-mono font-medium">{step.step}</span>{step.undo && <Badge variant="outline" className="text-warning">undo</Badge>}<span className="font-mono text-link">{step.activity}</span>
        <span className="ml-auto text-muted-foreground">{step.attempt > 1 && `${text('attemptShort', { attempt: step.attempt })} · `}{start && end ? `${end.getTime() - start.getTime()}ms` : ''}</span></div>
      {step.error && <div className="font-mono text-destructive">{step.errorType && `${step.errorType}: `}{step.error}</div>}
    </div>; })}
  </Panel>;
}

/* ---------- Rules ---------- */

function RuleDetail({ id, mode, onChanged, onDeleted }: { id: string; mode: Mode; onChanged: () => void; onDeleted: () => void }) {
  const client = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const [view, setView] = useState<'program' | 'source' | 'versions' | 'test' | 'runs'>('program'), [editing, setEditing] = useState(false), [epoch, setEpoch] = useState(0);
  const state = usePlatformQuery(`rule:${id}:${epoch}`, (signal) => client.getRule(create(api.GetRuleRequestSchema, { id }), { signal }));
  const rule = state.value?.rule, version = state.value?.version ?? rule?.current, definition = version?.definition;
  const changed = () => { setEpoch((value) => value + 1); onChanged(); };
  const control = (execute: (signal: AbortSignal) => Promise<unknown>, after = changed) => async (signal: AbortSignal) => {
    const result = await action.run((abort) => execute(AbortSignal.any([signal, abort]))); if (result === undefined) throw new Error('Rule outcome is unavailable'); after();
  };
  if (!rule) return <Panel><EmptyState title={state.error !== undefined ? text('error') : text('loading')} /></Panel>;
  return <>
    <Panel title={version?.name || id} description={<span className="font-mono">{definition?.event}</span>}
      actions={<>
        <StatusBadge tone={rule.paused ? 'warning' : 'success'}>{text(rule.paused ? 'paused' : 'active')}</StatusBadge>
        {rule.paused ? <ConfirmAction trigger={<><Play className="size-3.5" />{text('resume')}</>} title={text('resumeRuleConfirm')} description={text('resumeRuleHelp')} disabled={action.pending || action.disabled} onConfirm={control((signal) => client.resumeRule(create(api.ResumeRuleRequestSchema, { id }), { signal }))} />
          : <ConfirmAction trigger={<><Pause className="size-3.5" />{text('pause')}</>} title={text('pauseRuleConfirm')} description={text('pauseRuleHelp')} disabled={action.pending || action.disabled} onConfirm={control((signal) => client.pauseRule(create(api.PauseRuleRequestSchema, { id }), { signal }))} />}
        <Button size="sm" variant="outline" onClick={() => setEditing(true)}><Pencil />{text('edit')}</Button>
        <ConfirmAction trigger={<><Trash2 className="size-3.5" />{text('delete')}</>} title={text('deleteRuleConfirm')} description={text('deleteRuleHelp')} disabled={action.pending || action.disabled}
          onConfirm={control((signal) => client.deleteRule(create(api.DeleteRuleRequestSchema, { id }), { signal }), onDeleted)} />
      </>}>
      <KeyValueList items={[
        { label: text('version'), value: `v${version?.version ?? 0n}` }, { label: text('actor'), value: version?.author ?? '—', mono: true },
        { label: text('time'), value: <Timestamp value={date(version?.createdAt)} /> }, { label: text('when'), value: definition?.when || text('always'), mono: true },
        { label: text('id'), value: id, mono: true },
      ]} />
    </Panel>
    <TabBar aria-label={version?.name ?? id}>{(['program', 'source', 'versions', 'test', 'runs'] as const).map((name) =>
      <button key={name} type="button" aria-current={view === name ? 'page' : undefined} onClick={() => setView(name)} className="relative -mb-px inline-flex h-9 items-center border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground">{text(`tab_${name}`)}</button>)}</TabBar>
    {view === 'program' && <Panel>{definition ? <Pipeline source={definition.event} when={definition.when} steps={definition.steps} /> : <EmptyState className="py-6" title={text('deleted')} />}</Panel>}
    {view === 'source' && <Panel flush>{definition && <FormattedSource definition={definition} kind="rule" mode={mode} />}</Panel>}
    {view === 'versions' && <RuleVersions id={id} current={version?.version} mode={mode} onChanged={changed} />}
    {view === 'test' && <RuleTest id={id} mode={mode} />}
    {view === 'runs' && <RuleRuns id={id} mode={mode} />}
    <DetailDrawer open={editing} onOpenChange={setEditing} size="xl" title={text('editRule', { name: version?.name ?? id })} description={text('dslHelp')}>
      {editing && definition && <SourceEditor kind="rule" initial={definition} ruleId={id} ruleName={version?.name} mode={mode} onSaved={() => { setEditing(false); changed(); }} />}
    </DetailDrawer>
  </>;
}

function RuleEditorPanel({ service, mode, onSaved, onCancel }: { service: string; mode: Mode; onSaved: (id: string) => void; onCancel: () => void }) {
  const text = usePlatformText();
  return <Panel title={text('newRule')} description={text('dslHelp')} actions={<Button size="sm" variant="ghost" onClick={onCancel}>{text('cancelEdit')}</Button>}>
    <SourceEditor kind="rule" initial={create(api.RuleDefinitionSchema, { event: `${service}.` })} mode={mode} onSaved={(id) => { if (id) onSaved(id); }} />
  </Panel>;
}

function RuleVersions({ id, current, mode, onChanged }: { id: string; current?: bigint; mode: Mode; onChanged: () => void }) {
  const client = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const state = usePlatformQuery(`rule-versions:${id}:${current}`, (signal) => client.listRuleVersions(create(api.ListRuleVersionsRequestSchema, { id, pageSize: 100 }), { signal }));
  const [selected, setSelected] = useState<api.RuleVersion>();
  const currentDefinition = state.value?.versions.find((entry) => entry.version === current)?.definition;
  return <VersionList versions={(state.value?.versions ?? []).map((entry) => ({ version: entry.version, author: entry.author, comment: entry.comment, createdAt: entry.createdAt, deleted: entry.deleted, rollbackOf: entry.rollbackOf, raw: entry }))}
    current={current} loading={state.loading} onOpen={(entry) => setSelected(entry.raw)}>
    <DetailDrawer open={!!selected} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="xl" title={selected && `${selected.name} v${selected.version}`} description={selected?.comment}
      actions={selected && selected.version !== current && !selected.deleted && <ConfirmAction trigger={<><RotateCcw className="size-3.5" />{text('rollbackTo', { revision: selected.version.toString() })}</>} title={text('rollbackConfirm')} description={text('rollbackRuleHelp')} disabled={action.pending || action.disabled}
        onConfirm={async (signal) => { const result = await action.run((abort) => client.rollbackRule(create(api.RollbackRuleRequestSchema, { id, version: selected.version }), { signal: AbortSignal.any([signal, abort]) })); if (result === undefined) throw new Error('Rollback outcome is unavailable'); setSelected(undefined); onChanged(); }} />}>
      {selected && <DefinitionDiff kind="rule" before={selected.definition} after={currentDefinition} mode={mode} />}
    </DetailDrawer>
  </VersionList>;
}

function RuleTest({ id, mode }: { id: string; mode: Mode }) {
  const client = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<api.TestRuleResponse>();
  const [payload, setPayload] = useState('{}'), [dryRun, setDryRun] = useState(true);
  return <Panel title={<><FlaskConical className="size-4 text-muted-foreground" />{text('testRule')}</>} description={text('testRuleHelp')}>
    <div className="grid gap-3">
      <CodeEditor label={text('payload')} value={payload} onChange={setPayload} language="json" mode={mode} height={140} />
      <div className="flex items-center gap-3">
        <Button size="sm" disabled={action.pending || action.disabled} onClick={() => void action.run((signal) => client.testRule(create(api.TestRuleRequestSchema, { id, event: payload, dryRun }), { signal }))}><Play />{action.pending ? text('pending') : text('runTest')}</Button>
        <label className="flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={dryRun} onCheckedChange={setDryRun} />{text('dryRun')}</label>
      </div>
      {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
      {action.value && <div className="grid gap-2">
        {!!action.value.violations.length && <ul className="m-0 grid gap-1 pl-0 text-xs text-destructive" role="alert">{action.value.violations.map((violation, index) => <li key={index} className="list-none"><span className="font-mono">{violation.path}</span> {violation.message}</li>)}</ul>}
        {!action.value.violations.length && <div className="flex items-center gap-2 text-sm">{action.value.matched ? <><CircleCheck className="size-4 text-success" />{text('matched')}</> : <><CircleX className="size-4 text-muted-foreground" />{text('notMatched')}</>}</div>}
        {action.value.error && <pre className="m-0 font-mono text-xs whitespace-pre-wrap text-destructive">{action.value.error}</pre>}
        {action.value.result && <CallOutcome result={action.value.result} mode={mode} />}
      </div>}
    </div>
  </Panel>;
}

function RuleRuns({ id, mode }: { id: string; mode: Mode }) {
  const client = useClient(api.RuleServiceClient), text = usePlatformText();
  const [status, setStatus] = useState(api.RunStatus.UNSPECIFIED), [tests, setTests] = useState(false), [selected, setSelected] = useState<api.Run>();
  const state = usePlatformQuery(`rule-runs:${id}:${status}:${tests}`, (signal) => client.listRuleRuns(create(api.ListRuleRunsRequestSchema, { id, status, tests, pageSize: 50 }), { signal }));
  return <Panel title={text('runs')} count={state.value?.runs.length} flush actions={<label className="flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={tests} onCheckedChange={setTests} />{text('testRuns')}</label>}>
    <div className="border-b border-border px-3 py-2"><RunStatusFilter value={status} onChange={setStatus} /></div>
    <RunTable runs={state.value?.runs ?? []} loading={state.loading} onOpen={setSelected} />
    <RunDrawer run={selected} onClose={() => setSelected(undefined)} mode={mode} source={{
      load: async (run, signal) => (await client.getRuleRun(create(api.GetRuleRunRequestSchema, { id, ...run }), { signal })).run ?? create(api.GetRunResponseSchema),
      cancel: (run, signal) => client.cancelRuleRun(create(api.CancelRuleRunRequestSchema, { id, ...run }), { signal }),
    }} />
  </Panel>;
}

/* Kept for module authors and stories: open one binding or rule directly. */
export function BindingEditor({ hook, mode }: { hook: string; mode: Mode }) {
  return <div className="grid gap-4"><BindingDetail hook={hook} mode={mode} onChanged={() => undefined} /></div>;
}
export function RuleEditor({ id = '', event = '', mode }: { id?: string; event?: string; mode: Mode }) {
  const text = usePlatformText();
  return <div className="grid gap-4">{id ? <RuleDetail id={id} mode={mode} onChanged={() => undefined} onDeleted={() => undefined} />
    : <Panel title={text('newRule')}><SourceEditor kind="rule" initial={create(api.RuleDefinitionSchema, { event })} mode={mode} onSaved={() => undefined} /></Panel>}</div>;
}
