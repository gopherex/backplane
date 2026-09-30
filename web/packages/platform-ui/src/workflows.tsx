import { useCallback, useState } from 'react';
import { create, fromJsonString, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { structToNative } from '@gopherex/schemapb';
import { ScheduleOverlap } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';
import { useClient } from '@gopherex/backplane-react';
import { SchemaForm } from '@gopherex/backplane-schema-forms';
import { Badge, Button, ConfirmAction, DetailDrawer, EmptyState, FilterCombo, Panel, StatusBadge, Timestamp } from '@gopherex/backplane-ui';
import { CodeEditor, JSONViewer } from '@gopherex/backplane-editors';
import { CalendarClock, Pause, Play, RefreshCw, Workflow, Zap } from 'lucide-react';
import { MutationState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, durationText, enumLabel } from './format.js';
import { nativeJSON } from './serialization.js';
import { RunDrawer, RunStatusFilter, RunTable } from './runs.js';

/** Generic protobuf-JSON command with confirmation (technical fallback surfaces). */
export function CommandForm<S extends DescMessage>({ label, schema, initialValue, execute, mode, onComplete }: {
  label: string; schema: S; initialValue: MessageShape<S>; execute: (value: MessageShape<S>, signal: AbortSignal) => Promise<string>;
  mode: 'dark' | 'light'; onComplete?: () => void;
}) {
  const text = usePlatformText(), [value, setValue] = useState(toJsonString(schema, initialValue, { prettySpaces: 2 })), action = usePlatformAction<string>();
  return <section aria-label={label} className="grid gap-2"><CodeEditor label={label} mode={mode} language="json" value={value} onChange={setValue} disabled={action.pending} />
    <div><ConfirmAction trigger={label} title={text('confirmTitle')} description={text('confirmDescription')} disabled={action.pending || action.disabled} onConfirm={async (signal) => {
      const result = await action.run((abort) => execute(fromJsonString(schema, value), AbortSignal.any([signal, abort])));
      if (result === undefined) throw new Error('Action outcome is unavailable'); onComplete?.();
    }} /></div><MutationState action={action} />{action.value !== undefined && <JSONViewer label={text('output')} value={action.value} mode={mode} />}</section>;
}

/** Workflow runs of a service with status filter, paging and a run drawer. */
export function WorkflowRuns(props: { service: string; mode: 'dark' | 'light'; workflow?: string }) { return <ServiceRuns key={`${props.service}:${props.workflow ?? ''}`} {...props} />; }
function ServiceRuns({ service, mode, workflow: preset = '' }: { service: string; mode: 'dark' | 'light'; workflow?: string }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const [workflow, setWorkflow] = useState(preset);
  const loadWorkflows = useCallback(async (signal: AbortSignal) => (await client.listWorkflows(create(api.ListWorkflowsRequestSchema, { service }), { signal })).workflows.map((entry) => entry.name), [client, service]);
  const [status, setStatus] = useState(api.RunStatus.UNSPECIFIED), [pages, setPages] = useState<Uint8Array[]>([new Uint8Array()]), [selected, setSelected] = useState<api.Run>();
  const page = pages.at(-1)!;
  const state = usePlatformQuery(`runs:${service}:${workflow}:${status}:${Array.from(page)}`, (signal) => client.listRuns(create(api.ListRunsRequestSchema, { service, workflow, status, pageToken: page, pageSize: 50 }), { signal }));
  return <Panel fill title={<><Workflow className="size-4 text-muted-foreground" />{text('runs')}</>} count={state.value?.runs.length} flush
    actions={<Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh} disabled={state.loading}><RefreshCw className={state.loading ? 'animate-spin' : ''} /></Button>}
    footer={<>
      <span>{text('pageNumber', { page: pages.length })}</span>
      <span className="ml-auto flex gap-1">
        <Button size="xs" variant="ghost" disabled={pages.length < 2} onClick={() => setPages(pages.slice(0, -1))}>{text('newerPage')}</Button>
        <Button size="xs" variant="ghost" disabled={!state.value?.nextPageToken.length || state.loading} onClick={() => setPages([...pages, new Uint8Array(state.value!.nextPageToken)])}>{text('olderPage')}</Button>
      </span>
    </>}>
    <div className="sticky top-0 z-[2] flex flex-wrap items-center gap-2 border-b border-border bg-card px-3 py-2"><FilterCombo mono label={text('workflow')} value={workflow} onChange={(value) => { setWorkflow(value); setPages([new Uint8Array()]); }} loadOptions={loadWorkflows} />
      <RunStatusFilter value={status} onChange={(value) => { setStatus(value); setPages([new Uint8Array()]); }} /></div>
    {state.error !== undefined && <p className="m-0 px-3 py-2 text-xs text-destructive" role="alert">{text('error')}</p>}
    <RunTable runs={state.value?.runs ?? []} loading={state.loading} onOpen={setSelected} />
    <RunDrawer run={selected} onClose={() => setSelected(undefined)} mode={mode} />
  </Panel>;
}

/** Stand-alone run detail (used by module authors and stories). */
export function RunInspector({ workflowId, runId, mode }: { workflowId: string; runId: string; mode: 'dark' | 'light' }) {
  const [open, setOpen] = useState(true);
  return <>{!open && <Button variant="outline" size="sm" onClick={() => setOpen(true)}>{workflowId}</Button>}<RunDrawer run={open ? { workflowId, runId } : undefined} onClose={() => setOpen(false)} mode={mode} /></>;
}

/** Declared workflows with start-from-schema, then runs and schedules. */
export function WorkflowsPanel({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const definitions = usePlatformQuery(`workflows:${service}`, (signal) => client.listWorkflows(create(api.ListWorkflowsRequestSchema, { service }), { signal }));
  const [starting, setStarting] = useState<api.WorkflowDef>(), [filter, setFilter] = useState<string>();
  const workflows = definitions.value?.workflows ?? [];
  return <div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[320px_minmax(0,1fr)]">
    <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      <Panel title={text('workflows')} count={workflows.length} flush maxBodyHeight={280}>
        {!workflows.length && !definitions.loading && <EmptyState className="py-6" title={text('noWorkflows')} />}
        {workflows.map((workflow) => <div key={workflow.name} className={`group flex items-center gap-2 border-b border-border px-3 py-2 last:border-b-0 ${filter === workflow.name ? 'bg-primary/10' : ''}`}>
          <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setFilter(filter === workflow.name ? undefined : workflow.name)} aria-pressed={filter === workflow.name}>
            <span className="flex items-center gap-1.5"><span className="truncate font-mono text-xs">{workflow.name}</span>{workflow.kind === api.WorkflowKind.ACTIVITY && <Badge variant="outline">{text('activity')}</Badge>}</span>
            {workflow.description && <span className="block truncate text-xs text-muted-foreground">{workflow.description}</span>}
          </button>
          <Button size="xs" variant="outline" onClick={() => setStarting(workflow)}><Play />{text('startAction')}</Button>
        </div>)}
      </Panel>
      <SchedulesPanel service={service} mode={mode} />
    </div>
    <WorkflowRuns service={service} mode={mode} workflow={filter} />
    <DetailDrawer open={!!starting} onOpenChange={(open) => { if (!open) setStarting(undefined); }} title={text('startWorkflow', { name: starting?.name ?? '' })} description={starting?.description}>
      {starting && <StartWorkflow key={starting.name} service={service} workflow={starting} mode={mode} />}
    </DetailDrawer>
  </div>;
}

function StartWorkflow({ service, workflow, mode }: { service: string; workflow: api.WorkflowDef; mode: 'dark' | 'light' }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText(), action = usePlatformAction<api.StartWorkflowResponse>();
  const [input, setInput] = useState('{}'), [opened, setOpened] = useState<api.StartWorkflowResponse>();
  const start = (payload: string, signal: AbortSignal) => client.startWorkflow(create(api.StartWorkflowRequestSchema, { service, workflow: workflow.name, input: payload }), { signal });
  return <div className="grid gap-4">
    {workflow.input ? <SchemaForm schema={workflow.input} label={text('input')} submitLabel={text('startAction')} disabled={action.disabled || action.pending} onSubmit={async (baked, signal) => {
      const response = await action.run((abort) => start(nativeJSON(structToNative(baked.values)), AbortSignal.any([signal, abort])));
      if (response === undefined) throw new Error('Start outcome is unavailable');
    }} /> : <>
      <CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} height={160} />
      <div><Button disabled={action.pending || action.disabled} onClick={() => void action.run((signal) => start(input, signal))}><Play />{text('startAction')}</Button></div>
    </>}
    {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
    {action.value && <div className="flex items-center gap-2 rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-sm" role="status">
      <StatusBadge tone="success">{text('startedRun')}</StatusBadge><span className="min-w-0 flex-1 truncate font-mono text-xs">{action.value.workflowId}</span>
      <Button size="xs" variant="outline" onClick={() => setOpened(action.value)}>{text('openRun')}</Button></div>}
    <RunDrawer run={opened} onClose={() => setOpened(undefined)} mode={mode} />
  </div>;
}

export function SchedulesPanel(props: { service: string; mode: 'dark' | 'light' }) { return <ServiceSchedules key={props.service} {...props} />; }
function ServiceSchedules({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.ScheduleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const state = usePlatformQuery(`schedules:${service}`, (signal) => client.listSchedules(create(api.ListSchedulesRequestSchema, { service }), { signal }));
  const [opened, setOpened] = useState<{ workflowId: string; runId: string }>();
  const schedules = state.value?.schedules ?? [];
  const control = (schedule: api.ScheduleInfo, operation: 'pause' | 'resume' | 'trigger') => async (signal: AbortSignal) => {
    const response = await action.run(async (abort) => {
      const options = { signal: AbortSignal.any([signal, abort]) }, request = { service: schedule.service, name: schedule.name };
      if (operation === 'pause') return client.pauseSchedule(create(api.PauseScheduleRequestSchema, request), options);
      if (operation === 'resume') return client.unpauseSchedule(create(api.UnpauseScheduleRequestSchema, request), options);
      return client.triggerSchedule(create(api.TriggerScheduleRequestSchema, request), options);
    });
    if (response === undefined) throw new Error('Schedule outcome is unavailable'); state.refresh();
  };
  return <Panel title={<><CalendarClock className="size-4 text-muted-foreground" />{text('schedules')}</>} count={schedules.length} flush
    actions={<Button size="icon-sm" variant="ghost" aria-label={text('refresh')} onClick={state.refresh}><RefreshCw /></Button>}
    footer={action.error !== undefined ? <span className="text-destructive" role="alert">{text('mutationFailed')}</span> : undefined}>
    {!schedules.length && !state.loading && <EmptyState className="py-6" title={text('noSchedules')} />}
    {schedules.map((schedule) => {
      const paused = schedule.state?.paused, spec = schedule.declared?.spec, recent = schedule.state?.recentActions.at(-1), next = schedule.state?.nextActions[0];
      return <div key={schedule.id} className="grid gap-1.5 border-b border-border px-3 py-2.5 last:border-b-0">
        <div className="flex items-center gap-2"><span className="font-mono text-xs">{schedule.name}</span>
          <StatusBadge tone={paused ? 'warning' : 'success'}>{text(paused ? 'paused' : 'active')}</StatusBadge>
          <span className="ml-auto text-2xs text-muted-foreground">{text('runsCount', { count: Number(schedule.state?.actionCount ?? 0n) })}</span></div>
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <Badge variant="outline" className="font-mono">{spec?.case === 'cron' ? spec.value : spec?.case === 'every' ? `every ${durationText(spec.value)}` : '—'}</Badge>
          <span>→ {schedule.state?.workflowType || schedule.declared?.workflow}</span>
          {schedule.declared && <span>· {enumLabel(ScheduleOverlap, schedule.declared.overlap)}</span>}
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {next && !paused && <span>{text('nextRun')} <Timestamp value={date(next)} /></span>}
          {recent && <button type="button" className="text-link hover:underline" onClick={() => setOpened({ workflowId: recent.workflowId, runId: recent.runId })}>{text('lastRun')} <Timestamp value={date(recent.actualTime)} /></button>}
          {schedule.state?.note && <span className="italic">{schedule.state.note}</span>}
        </div>
        <div className="flex flex-wrap gap-1.5">
          {paused ? <ConfirmAction trigger={<><Play className="size-3.5" />{text('resume')}</>} title={text('resumeConfirm', { name: schedule.name })} description={text('resumeHelp')} disabled={action.pending || action.disabled} onConfirm={control(schedule, 'resume')} />
            : <ConfirmAction trigger={<><Pause className="size-3.5" />{text('pause')}</>} title={text('pauseConfirm', { name: schedule.name })} description={text('pauseHelp')} disabled={action.pending || action.disabled} onConfirm={control(schedule, 'pause')} />}
          <ConfirmAction trigger={<><Zap className="size-3.5" />{text('trigger')}</>} title={text('triggerConfirm', { name: schedule.name })} description={text('triggerHelp')} disabled={action.pending || action.disabled} onConfirm={control(schedule, 'trigger')} />
        </div>
      </div>;
    })}
    <RunDrawer run={opened} onClose={() => setOpened(undefined)} mode={mode} />
  </Panel>;
}
