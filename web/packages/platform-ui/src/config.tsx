import { useCallback, useEffect, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { ConfigServiceClient, WatchConfigRequestSchema, ListRevisionsRequestSchema, SaveRevisionRequestSchema, ValidateOverrideRequestSchema, RollbackRequestSchema, ServiceConfigSchema, type ServiceConfig, type Revision, type Violation } from '@gopherex/backplane-api';
import { useClient, useSnapshotWatch } from '@gopherex/backplane-react';
import { Button, Checkbox, ConfirmAction, DataTable, Input } from '@gopherex/backplane-ui';
import { CodeEditor, DiffViewer, JSONViewer } from '@gopherex/backplane-editors';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export function ConfigurationPanel({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(ConfigServiceClient), text = usePlatformText();
  const state = useSnapshotWatch(useCallback((signal: AbortSignal) => client.watchConfig(create(WatchConfigRequestSchema, { service }), { signal }), [client, service]));
  return <section aria-label={text('configuration')}>{state.status !== 'ready' && <p role="status">{text(state.status)}</p>}
    {state.value?.config && <ConfigDraft key={service} config={state.value.config} mode={mode} />}
  </section>;
}
function ConfigDraft({ config, mode }: { config: ServiceConfig; mode: 'dark' | 'light' }) {
  const client = useClient(ConfigServiceClient), text = usePlatformText(), action = usePlatformAction<{ revision?: Revision; violations: Violation[]; deliveryError: string }>();
  const [baseline, setBaseline] = useState(config.current), [values, setValues] = useState({ ...config.current?.values }), [comment, setComment] = useState('');
  const [violations, setViolations] = useState<Violation[]>([]), [valid, setValid] = useState(false), [selected, setSelected] = useState<Revision>();
  const [before, setBefore] = useState(0n), [older, setOlder] = useState<Revision[]>([]);
  const revisions = usePlatformQuery(`revisions:${config.service}:${before}:${config.current?.revision}`, (signal) => client.listRevisions(create(ListRevisionsRequestSchema, { service: config.service, before, pageSize: 50 }), { signal }));
  useEffect(() => { setOlder([]); setBefore(0n); }, [config.current?.revision]);
  const dirty = JSON.stringify(values) !== JSON.stringify(baseline?.values ?? {});
  useEffect(() => { if (!dirty) return; const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; }; window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn); }, [dirty]);
  const write = (key: string, value: string | undefined) => { setValid(false); setViolations([]); setValues((old) => { const next = { ...old }; if (value === undefined) delete next[key]; else Object.defineProperty(next, key, { value, enumerable: true, configurable: true, writable: true }); return next; }); };
  const save = async (signal: AbortSignal) => {
    const response = await client.saveRevision(create(SaveRevisionRequestSchema, { service: config.service, values, comment }), { signal });
    if (!signal.aborted) { setViolations(response.violations); if (response.revision) { setBaseline(response.revision); setValues({ ...response.revision.values }); revisions.refresh(); } }
    return response;
  };
  return <div style={{ display: 'grid', gap: 12 }}>
    {dirty && <p role="status">{text('dirty')}</p>}{(config.current?.revision ?? 0n) > (baseline?.revision ?? 0n) && <p role="status">{text('newer')}</p>}
    <MutationState action={action} />
    {config.live.map((path) => <fieldset key={path} disabled={action.pending || action.disabled} style={{ border: '1px solid var(--border)', padding: 12 }}><legend>{path}</legend>
      <Checkbox aria-label={`${text('override')} ${path}`} checked={Object.hasOwn(values, path)} onCheckedChange={(checked) => write(path, checked ? 'null' : undefined)} />
      {Object.hasOwn(values, path) && <CodeEditor label={path} language="json" mode={mode} value={values[path]} onChange={(value) => write(path, value)} height={100} />}
    </fieldset>)}
    <Input aria-label={text('comment')} value={comment} onChange={(event) => setComment(event.target.value)} />
    <div style={{ display: 'flex', gap: 8 }}><Button type="button" variant="outline" disabled={action.pending || action.disabled} onClick={() => void action.run(async (signal) => {
      const response = await client.validateOverride(create(ValidateOverrideRequestSchema, { service: config.service, values }), { signal });
      if (!signal.aborted) { setViolations(response.violations); setValid(!response.violations.length); } return { violations: response.violations, deliveryError: '' };
    })}>{text('validate')}</Button>
      <Button type="button" disabled={!dirty || action.pending || action.disabled} onClick={() => void action.run(save)}>{text('save')}</Button>
      <Button type="button" variant="outline" disabled={action.pending} onClick={() => { setBaseline(config.current); setValues({ ...config.current?.values }); setViolations([]); setValid(false); }}>{text('reset')}</Button>
    </div>{valid && <p role="status">{text('valid')}</p>}
    {!!violations.length && <ul role="alert">{violations.map((violation, index) => <li key={index}>{violation.path} {violation.instance}: {violation.message}</li>)}</ul>}
    {action.value?.deliveryError && <p role="status">{action.value.deliveryError}</p>}
    <QueryState state={revisions}><DataTable label={text('revisions')} data={[...older, ...(revisions.value?.revisions ?? [])]} getRowId={(revision) => revision.revision.toString()} onActivate={setSelected} columns={[
      { id: 'revision', label: text('revision'), value: (revision) => revision.revision }, { id: 'author', label: text('actor'), value: (revision) => revision.author }, { id: 'comment', label: text('comment'), value: (revision) => revision.comment, width: 360 },
    ]} /></QueryState>
    <Button type="button" variant="outline" disabled={!revisions.value?.nextBefore || revisions.loading} onClick={() => { setOlder([...older, ...(revisions.value?.revisions ?? [])]); setBefore(revisions.value!.nextBefore); }}>{text('more')}</Button>
    {selected && <><DiffViewer label={text('revisions')} before={JSON.stringify(selected.values, null, 2)} after={JSON.stringify(values, null, 2)} language="json" mode={mode} />
      <ConfirmAction trigger={text('rollback')} title={text('rollbackConfirm')} description={String(selected.revision)} onConfirm={async (signal) => {
        const response = await client.rollback(create(RollbackRequestSchema, { service: config.service, revision: selected.revision, comment }), { signal });
        if (!signal.aborted) { setViolations(response.violations); if (response.revision) { setBaseline(response.revision); setValues({ ...response.revision.values }); revisions.refresh(); } }
      }} /></>}
    <DataTable label={text('instances')} data={config.instances} getRowId={(instance) => instance.id} columns={[
      { id: 'id', label: text('instance'), value: (instance) => instance.id }, { id: 'applied', label: text('applied'), value: (instance) => instance.appliedRevision },
      { id: 'rejected', label: text('rejected'), value: (instance) => instance.rejectedRevision }, { id: 'error', label: text('reason'), value: (instance) => instance.error, width: 400 },
    ]} />
    <details><summary>{text('schema')}</summary><JSONViewer label={text('schema')} value={toJsonString(ServiceConfigSchema, config, { prettySpaces: 2 })} mode={mode} /></details>
  </div>;
}
