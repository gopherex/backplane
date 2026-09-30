import { useEffect, useState, type ReactNode } from 'react';
import { create, toJson } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Badge, Button, Combobox, Input, SectionLabel, StatusBadge, Textarea } from '@gopherex/backplane-ui';
import { InlineCodeEditor, JSONViewer } from '@gopherex/backplane-editors';
import { Trash2 } from 'lucide-react';
import { usePlatformAction } from '../runtime.js';
import { usePlatformText } from '../locales.js';
import { enumLabel } from '../format.js';
import { scopeOf, type WiringIndex } from './catalog.js';
import { completeCEL, definitionSource } from './complete.js';
import { removeStep, toYAML, validStepName, type Change, type Definition, type WiringKind } from './document.js';
import { stepTone } from './history.js';
import { shapeLabel, shapeOf } from './shape.js';

export interface StepInspectorProps {
  kind: WiringKind; definition?: Definition; step?: string; index: WiringIndex; analysis?: api.Analysis; run?: api.StepRun[]; mode?: 'dark' | 'light';
  onEdit: (path: readonly (string | number)[], value: unknown) => void; onChanges: (changes: Change[]) => void; onText: (text: string) => void; onSelect: (step: string | undefined) => void;
}

/** The selected node of the graph as a form; edits go to the draft as YAML edits. */
export function StepInspector(props: StepInspectorProps) {
  const { kind, definition, step } = props, text = usePlatformText();
  if (!definition) return <p className="m-0 text-xs text-muted-foreground">{text('graphNeedsYaml')}</p>;
  if (step === '$trigger') return <TriggerForm {...props} definition={definition} />;
  if (step === '$result' && kind === 'binding') return <ValueFields {...props} definition={definition} base={['result']} title={text('resultValue')}
    schema={props.index.hooks.get((definition as api.BindingDefinition).hook)?.value.output} value={json((definition as api.BindingDefinition).result)} />;
  if (!step || !definition.steps[step]) return <Overview {...props} definition={definition} />;
  return <StepForm key={step} {...props} definition={definition} name={step} />;
}

const json = (value?: api.BindingDefinition['result']): unknown => value ? toJson(ValueSchema, value) : undefined;

function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return <label className="grid gap-1 text-xs"><span className="flex items-baseline gap-2 text-muted-foreground">{label}{hint && <span className="ml-auto truncate text-2xs text-muted-foreground">{hint}</span>}</span>{children}</label>;
}

/** A text input that commits on blur or Enter, not on every keystroke (one undo step per edit). */
function CommitInput({ value, onCommit, ...props }: { value: string; onCommit: (value: string) => void } & Omit<Parameters<typeof Input>[0], 'value' | 'onChange'>) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return <Input {...props} value={draft} onChange={(event) => setDraft(event.target.value)} onBlur={() => { if (draft !== value) onCommit(draft); }}
    onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); if (draft !== value) onCommit(draft); } }} />;
}

/** A CEL expression edited inline with completion; commits when it loses focus. */
function CelInput({ label, value, onCommit, scope, mode }: { label: string; value: string; onCommit: (value: string) => void; scope: ReturnType<typeof scopeOf>; mode?: 'dark' | 'light' }) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return <div onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null) && draft !== value) onCommit(draft); }}>
    <InlineCodeEditor label={label} value={draft} onChange={setDraft} language="cel" mode={mode ?? 'dark'} hideMessages onSubmit={() => { if (draft !== value) onCommit(draft); }}
      complete={async ({ value: current, position }) => completeCEL(current.slice(0, position), position, scope)} />
  </div>;
}

function Overview({ kind, definition, index, mode, onEdit }: StepInspectorProps & { definition: Definition }) {
  const text = usePlatformText(), source = definitionSource(kind, definition)!;
  return <div className="grid gap-3">
    <p className="m-0 text-xs text-muted-foreground">{text('inspectorHelp')}</p>
    <Field label={text('description')}><Textarea className="min-h-16 text-xs" defaultValue={definition.description} onBlur={(event) => { if (event.target.value !== definition.description) onEdit(['description'], event.target.value || undefined); }} /></Field>
    {kind === 'rule' && <Field label={text('when')} hint={text('ruleWhenHelp')}>
      <CelInput label={text('when')} value={(definition as api.RuleDefinition).when} scope={scopeOf(index, source, {}, true)} mode={mode} onCommit={(value) => onEdit(['when'], value || undefined)} /></Field>}
    <div className="grid gap-1 text-xs"><SectionLabel>{text('stepsTitle')}</SectionLabel>
      <span className="text-muted-foreground">{text('stepsCount', { count: Object.keys(definition.steps).length })}</span></div>
  </div>;
}

function TriggerForm({ kind, definition, index, mode, onEdit }: StepInspectorProps & { definition: Definition }) {
  const text = usePlatformText(), source = definitionSource(kind, definition)!;
  const schema = kind === 'binding' ? index.hooks.get((source as { hook: string }).hook)?.value.input : index.events.get((source as { event: string }).event)?.value.schema;
  const fields = shapeOf(schema).fields ?? [];
  return <div className="grid gap-3">
    <Field label={text(kind === 'binding' ? 'hook' : 'event')}><span className="font-mono text-sm">{'hook' in source ? source.hook : source.event}</span></Field>
    {kind === 'rule' && <Field label={text('when')} hint={text('ruleWhenHelp')}>
      <CelInput label={text('when')} value={(definition as api.RuleDefinition).when} scope={scopeOf(index, source, {}, true)} mode={mode} onCommit={(value) => onEdit(['when'], value || undefined)} /></Field>}
    <SectionLabel>{text(kind === 'binding' ? 'hookInput' : 'eventInput')}</SectionLabel>
    {!fields.length ? <p className="m-0 text-xs text-muted-foreground">{text('noSchema')}</p> : <ul className="m-0 grid list-none gap-1 p-0 text-xs">{fields.map((field) =>
      <li key={field.name} className="flex items-baseline gap-2"><span className="font-mono">{field.name}{field.required && <span className="text-destructive">*</span>}</span><span className="font-mono text-muted-foreground">{shapeLabel(field.shape)}</span>
        {field.description && <span className="min-w-0 truncate text-muted-foreground" title={field.description}>{field.description}</span>}</li>)}</ul>}
  </div>;
}

/** The fields of a value (a step's input, a binding's result) against its schema: one CEL input per field. */
function ValueFields({ kind, definition, index, mode, onEdit, base, title, schema, value, step }: StepInspectorProps & { definition: Definition; base: (string | number)[]; title: string; schema?: Parameters<typeof shapeOf>[0]; value: unknown }) {
  const text = usePlatformText(), source = definitionSource(kind, definition)!;
  const scope = scopeOf(index, source, Object.fromEntries(Object.entries(definition.steps).filter(([name]) => name !== step)));
  const fields = shapeOf(schema).fields ?? [], object = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
  const extra = object ? Object.keys(object).filter((name) => !fields.some((field) => field.name === name)) : [];
  return <div className="grid gap-2">
    <SectionLabel>{title}</SectionLabel>
    {typeof value === 'string' && <Field label={text('wholeValue')} hint={text('wholeValueHelp')}><CelInput label={title} value={value} scope={scope} mode={mode} onCommit={(next) => onEdit(base, next || undefined)} /></Field>}
    {!schema && typeof value !== 'string' && <p className="m-0 text-xs text-muted-foreground">{text('noSchema')}</p>}
    {typeof value !== 'string' && [...fields.map((field) => ({ name: field.name, type: shapeLabel(field.shape), required: field.required, description: field.description })), ...extra.map((name) => ({ name, type: '?', required: false, description: text('undeclaredField') }))].map((field) => {
      const current = object?.[field.name];
      return <Field key={field.name} label={<span className="font-mono text-foreground">{field.name}{field.required && <span className="text-destructive">*</span>} <span className="text-muted-foreground">{field.type}</span></span>} hint={field.description}>
        {current === undefined || typeof current === 'string'
          ? <CelInput label={field.name} value={current ?? ''} scope={scope} mode={mode} onCommit={(next) => onEdit([...base, field.name], next || undefined)} />
          : <div className="rounded-md border border-border bg-raised px-2 py-1 font-mono text-2xs text-muted-foreground" title={text('editInYaml')}>{JSON.stringify(current)}</div>}
      </Field>;
    })}
  </div>;
}

function StepForm(props: StepInspectorProps & { definition: Definition; name: string }) {
  const { kind, definition, name, index, analysis, run, mode, onEdit, onChanges, onText, onSelect } = props;
  const wiring = useClient(api.WiringServiceClient), text = usePlatformText(), rename = usePlatformAction<api.RenameStepResponse>();
  const step = definition.steps[name]!, source = definitionSource(kind, definition)!;
  const activity = index.activities.get(step.activity)?.value, undo = index.activities.get(step.undo)?.value;
  const facts = analysis?.steps.find((entry) => entry.name === name);
  const calls = run?.filter((call) => call.step === name) ?? [];
  const others = Object.keys(definition.steps).filter((other) => other !== name).sort();
  const activities = [...index.activities.keys()].sort().map((full) => ({ value: full, label: full }));
  const scope = scopeOf(index, source, Object.fromEntries(Object.entries(definition.steps).filter(([other]) => other !== name)));
  const doRename = (to: string) => void rename.run(async (signal) => {
    const response = await wiring.renameStep(create(api.RenameStepRequestSchema, { from: name, to, definition: kind === 'binding' ? { case: 'binding', value: definition as api.BindingDefinition } : { case: 'rule', value: definition as api.RuleDefinition } }), { signal });
    if (response.definition.value) { onText(toYAML(kind, response.definition.value)); onSelect(to); }
    return response;
  });
  const retry = step.retry, duration = (value?: { seconds: bigint; nanos: number }) => value ? `${Number(value.seconds) + value.nanos / 1e9}s` : '';
  return <div className="grid gap-3">
    <Field label={text('stepName')} hint={facts && facts.level >= 0 ? text('levelN', { level: facts.level }) : undefined}>
      <CommitInput className="h-8 font-mono text-xs" aria-label={text('stepName')} value={name} onCommit={(to) => { if (validStepName(to)) doRename(to); }} />
    </Field>
    {!!rename.value?.violations.length && <p className="m-0 text-xs text-destructive" role="alert">{rename.value.violations[0]!.message}</p>}
    <Field label={text('activity')} hint={activity?.description}>
      <Combobox label={text('activity')} options={activities} values={step.activity ? [step.activity] : []} onValuesChange={(values) => { if (values[0]) onEdit(['steps', name, 'activity'], values[0]); }} placeholder={text('pickActivity')} />
    </Field>
    <Field label={text('description')}><CommitInput className="h-8 text-xs" aria-label={text('description')} value={step.description} onCommit={(value) => onEdit(['steps', name, 'description'], value || undefined)} /></Field>
    <Field label={text('when')} hint={text('whenHelp')}><CelInput label={text('when')} value={step.when} scope={scope} mode={mode} onCommit={(value) => onEdit(['steps', name, 'when'], value || undefined)} /></Field>
    <ValueFields {...props} definition={definition} base={['steps', name, 'input']} title={text('input')} schema={activity?.input} value={json(step.input)} step={name} />
    <div className="grid gap-1 text-xs"><SectionLabel>{text('runsAfter')}</SectionLabel>
      <div className="flex flex-wrap gap-1">{others.length ? others.map((other) => { const on = step.after.includes(other); return <button key={other} type="button" aria-pressed={on}
        onClick={() => onEdit(['steps', name, 'after'], on ? step.after.filter((entry) => entry !== other) : [...step.after, other])}
        className="h-6 rounded-sm border border-border px-2 font-mono text-2xs text-muted-foreground aria-pressed:border-primary/50 aria-pressed:bg-primary/10 aria-pressed:text-foreground">{other}</button>; })
        : <span className="text-muted-foreground">{text('noOtherSteps')}</span>}</div>
      {facts && <span className="text-2xs text-muted-foreground">{text('dependsOn')}: <span className="font-mono">{[...new Set([...facts.data, ...facts.after, ...facts.when])].join(', ') || '—'}</span></span>}
    </div>
    <Field label={text('undoActivity')} hint={text('undoHelp')}>
      <Combobox label={text('undoActivity')} options={activities} values={step.undo ? [step.undo] : []} onValuesChange={(values) => onEdit(['steps', name, 'undo'], values[0] || undefined)} placeholder={text('none')} />
    </Field>
    {step.undo && <ValueFields {...props} definition={definition} base={['steps', name, 'undoInput']} title={text('undoInput')} schema={undo?.input} value={json(step.undoInput)} step={name} />}
    <div className="grid grid-cols-2 gap-2">
      <Field label={text('attempts')}><CommitInput className="h-8 text-xs" inputMode="numeric" placeholder={String(activity?.retry?.attempts || 3)} value={retry?.attempts ? String(retry.attempts) : ''} onCommit={(value) => onEdit(['steps', name, 'retry', 'attempts'], value ? Number(value) : undefined)} /></Field>
      <Field label={text('backoff')}><CommitInput className="h-8 text-xs" inputMode="decimal" placeholder="2" value={retry?.backoff ? String(retry.backoff) : ''} onCommit={(value) => onEdit(['steps', name, 'retry', 'backoff'], value ? Number(value) : undefined)} /></Field>
      <Field label={text('initialInterval')}><CommitInput className="h-8 text-xs" placeholder="1s" value={duration(retry?.initialInterval)} onCommit={(value) => onEdit(['steps', name, 'retry', 'initialInterval'], value || undefined)} /></Field>
      <Field label={text('maxInterval')}><CommitInput className="h-8 text-xs" placeholder="30s" value={duration(retry?.maxInterval)} onCommit={(value) => onEdit(['steps', name, 'retry', 'maxInterval'], value || undefined)} /></Field>
      <Field label={text('startToClose')}><CommitInput className="h-8 text-xs" placeholder={activity?.startToClose ? duration(activity.startToClose) : '30s'} value={duration(step.startToClose)} onCommit={(value) => onEdit(['steps', name, 'startToClose'], value || undefined)} /></Field>
      <Field label={text('heartbeat')}><CommitInput className="h-8 text-xs" placeholder={activity?.heartbeat ? duration(activity.heartbeat) : '—'} value={duration(step.heartbeat)} onCommit={(value) => onEdit(['steps', name, 'heartbeat'], value || undefined)} /></Field>
    </div>
    {!!calls.length && <div className="grid gap-2"><SectionLabel>{text('lastRun')}</SectionLabel>{calls.map((call, at) => <div key={at} className="grid gap-1 rounded-md border border-border p-2 text-xs">
      <div className="flex items-center gap-2"><StatusBadge tone={stepTone[call.status]}>{enumLabel(api.StepRunStatus, call.status)}</StatusBadge>{call.undo && <Badge variant="outline">undo</Badge>}
        <span className="font-mono text-link">{call.activity}</span>{call.attempt > 1 && <span className="ml-auto text-muted-foreground">{text('attemptShort', { attempt: call.attempt })}</span>}</div>
      {call.error && <div className="font-mono text-destructive">{call.error}</div>}
      {call.input && <JSONViewer label={text('input')} value={call.input} mode={mode ?? 'dark'} height={90} />}
      {call.output && <JSONViewer label={text('output')} value={call.output} mode={mode ?? 'dark'} height={90} />}
    </div>)}</div>}
    <Button size="sm" variant="outline" className="justify-self-start text-destructive" onClick={() => { onChanges(removeStep(definition, name)); onSelect(undefined); }}><Trash2 />{text('deleteStep')}</Button>
  </div>;
}

