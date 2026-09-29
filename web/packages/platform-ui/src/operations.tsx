import { nativeJSON } from './serialization.js';
export { nativeJSON } from './serialization.js';
import { useMemo, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { structToNative } from '@gopherex/schemapb';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Button, NativeSelect } from '@gopherex/backplane-ui';
import { CodeEditor, JSONViewer } from '@gopherex/backplane-editors';
import { SchemaForm } from '@gopherex/backplane-schema-forms';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export type OperationKind = 'hook' | 'activity' | 'event' | 'workflow';
export function OperationSelector({ service, kind, value, onChange }: { service: string; kind: OperationKind; value: string; onChange: (value: string) => void }) {
  const client = useClient(api.CatalogServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`operations:${service}`, (signal) => client.getService(create(api.GetServiceRequestSchema, { name: service }), { signal }));
  const definitions = state.value?.latest?.[kind === 'hook' ? 'hooks' : kind === 'activity' ? 'activities' : kind === 'event' ? 'events' : 'workflows'] ?? [];
  return <QueryState state={state}><NativeSelect aria-label={text(kind)} value={value} onChange={(event) => onChange(event.target.value)}><option value="">{text('select')}</option>
    {definitions.map((definition) => <option key={definition.name} value={kind === 'workflow' ? definition.name : `${service}.${definition.name}`}>{definition.name}</option>)}
  </NativeSelect></QueryState>;
}
export function ServiceOperations({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const catalog = useClient(api.CatalogServiceClient), calls = useClient(api.CallServiceClient), events = useClient(api.EventServiceClient), workflows = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`operations:${service}`, (signal) => catalog.getService(create(api.GetServiceRequestSchema, { name: service }), { signal }));
  const [kind, setKind] = useState<OperationKind>('hook'), [name, setName] = useState(''), [input, setInput] = useState('{}');
  const action = usePlatformAction<string>();
  const options = useMemo(() => {
    const manifest = state.value?.latest; if (!manifest) return [];
    return kind === 'event' ? manifest.events.map((event) => ({ name: event.name, input: event.schema })) : kind === 'hook' ? manifest.hooks : kind === 'activity' ? manifest.activities : manifest.workflows;
  }, [state.value, kind]);
  const selected = options.find((option) => option.name === name);
  const execute = async (input: string, signal: AbortSignal) => {
    if (kind === 'hook') return toJsonString(api.CallHookResponseSchema, await calls.callHook(create(api.CallHookRequestSchema, { hook: `${service}.${name}`, input }), { signal }), { prettySpaces: 2 });
    if (kind === 'activity') return toJsonString(api.RunActivityResponseSchema, await calls.runActivity(create(api.RunActivityRequestSchema, { activity: `${service}.${name}`, input }), { signal }), { prettySpaces: 2 });
    if (kind === 'event') return toJsonString(api.PublishTestEventResponseSchema, await events.publishTestEvent(create(api.PublishTestEventRequestSchema, { event: `${service}.${name}`, payload: input }), { signal }), { prettySpaces: 2 });
    return toJsonString(api.StartWorkflowResponseSchema, await workflows.startWorkflow(create(api.StartWorkflowRequestSchema, { service, workflow: name, input }), { signal }), { prettySpaces: 2 });
  };
  return <QueryState state={state}><section style={{ display: 'grid', gap: 12 }}>
    <NativeSelect aria-label={text('selectAction')} value={kind} disabled={action.pending} onChange={(event) => { setKind(event.target.value as OperationKind); setName(''); }}>
      {(['hook', 'activity', 'event', 'workflow'] as const).map((kind) => <option key={kind} value={kind}>{text(kind)}</option>)}
    </NativeSelect><NativeSelect aria-label={text('name')} value={name} disabled={action.pending} onChange={(event) => setName(event.target.value)}><option value="">{text('select')}</option>{options.map((option) => <option key={option.name} value={option.name}>{option.name}</option>)}</NativeSelect>
    {selected?.input ? <SchemaForm key={`${service}:${kind}:${name}`} schema={selected.input} label={text('input')} submitLabel={text('execute')} disabled={action.disabled || action.pending} onSubmit={async (baked, signal) => {
      const response = await action.run((abort) => {
        const combined = AbortSignal.any([signal, abort]); return execute(nativeJSON(structToNative(baked.values)), combined);
      });
      if (response === undefined) throw new Error('Operation outcome is unavailable');
    }} /> : <><CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} disabled={action.pending} /><Button type="button" disabled={!selected || action.pending || action.disabled} onClick={() => void action.run((signal) => execute(input, signal))}>{text('execute')}</Button></>}
    <MutationState action={action} />{action.value !== undefined && <JSONViewer label={text('output')} value={action.value} mode={mode} />}
  </section></QueryState>;
}
