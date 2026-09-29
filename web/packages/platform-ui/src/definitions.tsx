import { useEffect, useState } from 'react';
import { create, fromJsonString, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Button, Input } from '@gopherex/backplane-ui';
import { CodeEditor, DiffViewer } from '@gopherex/backplane-editors';
import { QueryState, MutationState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export function DefinitionEditor<S extends DescMessage>({ schema, initialValue, mode, validate, save }: {
  schema: S; initialValue: MessageShape<S>; mode: 'dark' | 'light';
  validate: (definition: MessageShape<S>, signal: AbortSignal) => Promise<readonly { path: string; message: string }[]>;
  save: (definition: MessageShape<S>, comment: string, signal: AbortSignal) => Promise<{ violations: readonly { path: string; message: string }[]; version?: bigint }>;
}) {
  const text = usePlatformText(), [baseline, setBaseline] = useState(toJsonString(schema, initialValue, { prettySpaces: 2 }));
  const [value, setValue] = useState(baseline), [comment, setComment] = useState(''), [violations, setViolations] = useState<readonly { path: string; message: string }[]>([]), [valid, setValid] = useState(false);
  const action = usePlatformAction<bigint | undefined>(); const dirty = value !== baseline;
  useEffect(() => { if (!dirty) return; const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; }; window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn); }, [dirty]);
  return <div style={{ display: 'grid', gap: 12 }}>{dirty && <p role="status">{text('dirty')}</p>}
    <CodeEditor label={text('definition')} value={value} onChange={(value) => { setValue(value); setValid(false); setViolations([]); }} language="json" mode={mode} disabled={action.pending} height={360} />
    <Input aria-label={text('comment')} value={comment} onChange={(event) => setComment(event.target.value)} disabled={action.pending} />
    <div style={{ display: 'flex', gap: 8 }}><Button type="button" variant="outline" disabled={action.pending || action.disabled} onClick={() => void action.run(async (signal) => {
      const violations = await validate(fromJsonString(schema, value), signal); if (!signal.aborted) { setViolations(violations); setValid(!violations.length); } return undefined;
    })}>{text('validate')}</Button><Button type="button" disabled={action.pending || action.disabled} onClick={() => void action.run(async (signal) => {
      const response = await save(fromJsonString(schema, value), comment, signal);
      if (!signal.aborted) { setViolations(response.violations); if (response.version !== undefined) setBaseline(value); } return response.version;
    })}>{text('save')}</Button><Button type="button" variant="outline" disabled={action.pending || !dirty} onClick={() => setValue(baseline)}>{text('reset')}</Button></div>
    <MutationState action={action} />{valid && <p role="status">{text('valid')}</p>}{action.value !== undefined && <p role="status">{text('saved')}: {String(action.value)}</p>}
    {!!violations.length && <ul role="alert">{violations.map((item, index) => <li key={index}>{item.path}: {item.message}</li>)}</ul>}
    {dirty && <DiffViewer label={text('definition')} before={baseline} after={value} mode={mode} language="json" />}
  </div>;
}
export function BindingEditor({ hook, mode }: { hook: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.BindingServiceClient);
  const state = usePlatformQuery(`binding:${hook}`, (signal) => client.getBinding(create(api.GetBindingRequestSchema, { hook }), { signal }));
  return <QueryState state={state}>{state.value && <DefinitionEditor key={hook} schema={api.BindingDefinitionSchema} initialValue={state.value?.version?.definition ?? create(api.BindingDefinitionSchema, { hook })} mode={mode}
    validate={async (definition, signal) => (await client.validateBinding(create(api.ValidateBindingRequestSchema, { definition }), { signal })).violations}
    save={async (definition, comment, signal) => { const response = await client.saveBinding(create(api.SaveBindingRequestSchema, { definition, comment }), { signal }); return { violations: response.violations, version: response.version?.version }; }} />}</QueryState>;
}
export function RuleEditor({ id = '', event = '', mode }: { id?: string; event?: string; mode: 'dark' | 'light' }) {
  return <RuleDraft key={`${id}:${event}`} id={id} event={event} mode={mode} />;
}
function RuleDraft({ id, event, mode }: { id: string; event: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.RuleServiceClient), text = usePlatformText(), [name, setName] = useState<string>();
  const state = usePlatformQuery(`rule:${id}`, (signal) => id ? client.getRule(create(api.GetRuleRequestSchema, { id }), { signal }) : Promise.resolve(create(api.GetRuleResponseSchema)));
  const [createdId, setCreatedId] = useState('');
  return <QueryState state={state}>{state.value && <><Input aria-label={text('name')} value={name ?? state.value?.rule?.current?.name ?? ''} onChange={(event) => setName(event.target.value)} />
    <DefinitionEditor key={`${id}:${event}`} schema={api.RuleDefinitionSchema} initialValue={state.value?.rule?.current?.definition ?? create(api.RuleDefinitionSchema, { event })} mode={mode}
      validate={async (definition, signal) => (await client.validateRule(create(api.ValidateRuleRequestSchema, { definition }), { signal })).violations}
      save={async (definition, comment, signal) => { const response = await client.saveRule(create(api.SaveRuleRequestSchema, { id: id || createdId, name: name ?? state.value?.rule?.current?.name, definition, comment }), { signal }); if (!signal.aborted && response.version) setCreatedId(response.version.ruleId); return { violations: response.violations, version: response.version?.version }; }} />
  </>}</QueryState>;
}
