import { useState } from 'react';
import { create, fromJsonString, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Button, ConfirmAction, DataTable, Input } from '@gopherex/backplane-ui';
import { CodeEditor, JSONViewer } from '@gopherex/backplane-editors';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export function CommandForm<S extends DescMessage>({ label, schema, initialValue, execute, mode, onComplete }: {
  label: string; schema: S; initialValue: MessageShape<S>; execute: (value: MessageShape<S>, signal: AbortSignal) => Promise<string>;
  mode: 'dark' | 'light'; onComplete?: () => void;
}) {
  const text = usePlatformText(), [value, setValue] = useState(toJsonString(schema, initialValue, { prettySpaces: 2 })), action = usePlatformAction<string>();
  return <section aria-label={label} style={{ display: 'grid', gap: 8 }}><CodeEditor label={label} mode={mode} language="json" value={value} onChange={setValue} disabled={action.pending} />
    <ConfirmAction trigger={label} title={text('confirmTitle')} description={text('confirmDescription')} disabled={action.pending || action.disabled} onConfirm={async (signal) => {
      const result = await action.run((abort) => execute(fromJsonString(schema, value), AbortSignal.any([signal, abort])));
      if (result === undefined) throw new Error('Action outcome is unavailable'); onComplete?.();
    }} /><MutationState action={action} />{action.value !== undefined && <JSONViewer label={text('output')} value={action.value} mode={mode} />}</section>;
}
export function WorkflowRuns(props: { service: string; mode: 'dark' | 'light' }) { return <ServiceRuns key={props.service} {...props} />; }
function ServiceRuns({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText(), [workflow, setWorkflow] = useState(''), [page, setPage] = useState(new Uint8Array()), [selected, setSelected] = useState<api.Run>();
  const state = usePlatformQuery(`runs:${service}:${workflow}:${Array.from(page)}`, (signal) => client.listRuns(create(api.ListRunsRequestSchema, { service, workflow, pageToken: page, pageSize: 100 }), { signal }));
  return <section style={{ display: 'grid', gap: 12 }}><Input aria-label={text('workflow')} value={workflow} onChange={(event) => { setWorkflow(event.target.value); setPage(new Uint8Array()); }} />
    <Button type="button" variant="outline" onClick={state.refresh}>{text('refresh')}</Button><QueryState state={state}><DataTable label={text('runs')} data={state.value?.runs ?? []} getRowId={(run) => run.runId} onActivate={setSelected} columns={[
      { id: 'workflow', label: text('workflow'), value: (run) => run.workflowId, width: 320 }, { id: 'run', label: text('runId'), value: (run) => run.runId, width: 320 },
      { id: 'status', label: text('status'), value: (run) => api.RunStatus[run.status] }, { id: 'history', label: text('history'), value: (run) => run.historyLength },
    ]} /></QueryState><Button type="button" variant="outline" disabled={!state.value?.nextPageToken.length || state.loading} onClick={() => setPage(new Uint8Array(state.value!.nextPageToken))}>{text('more')}</Button>
    {selected && <RunInspector key={selected.runId} workflowId={selected.workflowId} runId={selected.runId} mode={mode} />}
  </section>;
}
export function RunInspector({ workflowId, runId, mode }: { workflowId: string; runId: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`run:${workflowId}:${runId}`, (signal) => client.getRun(create(api.GetRunRequestSchema, { workflowId, runId }), { signal }));
  return <QueryState state={state}>{state.value && <section style={{ display: 'grid', gap: 12 }}>
    <Button type="button" variant="outline" onClick={state.refresh}>{text('refresh')}</Button>
    {state.value.failure && <p role="alert">{state.value.failureType}: {state.value.failure}</p>}
    {state.value.truncated && <p role="status">{text('partial')}</p>}
    <JSONViewer label={text('input')} value={state.value.input || 'null'} mode={mode} /><JSONViewer label={text('output')} value={state.value.result || 'null'} mode={mode} />
    <DataTable label={text('history')} data={state.value.history} getRowId={(event) => String(event.id)} columns={[
      { id: 'id', label: text('sequence'), value: (event) => event.id }, { id: 'type', label: text('action'), value: (event) => event.type }, { id: 'summary', label: text('detail'), value: (event) => event.summary, width: 500 }, { id: 'failure', label: text('failure'), value: (event) => event.failure },
    ]} />
    <details><summary>{text('cancel')}</summary><CommandForm label={text('cancel')} schema={api.CancelRunRequestSchema} initialValue={create(api.CancelRunRequestSchema, { workflowId, runId })} mode={mode} onComplete={state.refresh} execute={async (request, signal) => toJsonString(api.CancelRunResponseSchema, await client.cancelRun(request, { signal }))} /></details>
    <details><summary>{text('terminate')}</summary><CommandForm label={text('terminate')} schema={api.TerminateRunRequestSchema} initialValue={create(api.TerminateRunRequestSchema, { workflowId, runId, reason: '' })} mode={mode} onComplete={state.refresh} execute={async (request, signal) => toJsonString(api.TerminateRunResponseSchema, await client.terminateRun(request, { signal }))} /></details>
    <details><summary>{text('signalRun')}</summary><CommandForm label={text('signalRun')} schema={api.SignalRunRequestSchema} initialValue={create(api.SignalRunRequestSchema, { workflowId, runId })} mode={mode} onComplete={state.refresh} execute={async (request, signal) => toJsonString(api.SignalRunResponseSchema, await client.signalRun(request, { signal }))} /></details>
    <details><summary>{text('raw')}</summary><JSONViewer label={text('raw')} value={toJsonString(api.GetRunResponseSchema, state.value, { prettySpaces: 2 })} mode={mode} /></details>
  </section>}</QueryState>;
}
export function SchedulesPanel(props: { service: string; mode: 'dark' | 'light' }) { return <ServiceSchedules key={props.service} {...props} />; }
function ServiceSchedules({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.ScheduleServiceClient), text = usePlatformText(), action = usePlatformAction<string>();
  const state = usePlatformQuery(`schedules:${service}`, (signal) => client.listSchedules(create(api.ListSchedulesRequestSchema, { service }), { signal }));
  const [selectedId, setSelectedId] = useState('');
  const selected = state.value?.schedules.find((schedule) => schedule.id === selectedId);
  return <QueryState state={state}><section style={{ display: 'grid', gap: 12 }}><MutationState action={action} />
    <Button type="button" variant="outline" onClick={state.refresh}>{text('refresh')}</Button><DataTable label={text('schedules')} data={state.value?.schedules ?? []} getRowId={(schedule) => schedule.id} onActivate={(schedule) => setSelectedId(schedule.id)} columns={[
      { id: 'name', label: text('name'), value: (schedule) => schedule.name }, { id: 'workflow', label: text('workflow'), value: (schedule) => schedule.state?.workflowType },
      { id: 'paused', label: text('status'), value: (schedule) => schedule.state?.paused }, { id: 'count', label: text('sequence'), value: (schedule) => schedule.state?.actionCount },
    ]} />
    {selected && <><JSONViewer label={text('detail')} value={toJsonString(api.ScheduleInfoSchema, selected, { prettySpaces: 2 })} mode={mode} />
      <div style={{ display: 'flex', gap: 8 }}>{(['pause', 'resume', 'trigger'] as const).map((operation) => <ConfirmAction key={operation} trigger={text(operation)} title={text('confirmTitle')} description={`${selected.service}/${selected.name}`} disabled={action.pending || action.disabled} onConfirm={async (signal) => {
        const response = await action.run(async (abort) => {
          const options = { signal: AbortSignal.any([signal, abort]) }, request = { service: selected.service, name: selected.name };
          if (operation === 'pause') return toJsonString(api.PauseScheduleResponseSchema, await client.pauseSchedule(create(api.PauseScheduleRequestSchema, request), options));
          if (operation === 'resume') return toJsonString(api.UnpauseScheduleResponseSchema, await client.unpauseSchedule(create(api.UnpauseScheduleRequestSchema, request), options));
          return toJsonString(api.TriggerScheduleResponseSchema, await client.triggerSchedule(create(api.TriggerScheduleRequestSchema, request), options));
        });
        if (response === undefined) throw new Error('Schedule outcome is unavailable'); state.refresh();
      }} />)}</div></>}
  </section></QueryState>;
}
