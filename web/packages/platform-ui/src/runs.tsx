import { useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Badge, Button, ConfirmAction, DetailDrawer, Duration, EmptyState, Input, KeyValueList, Panel, Skeleton, StatusBadge, Timestamp, type StatusTone } from '@gopherex/backplane-ui';
import { CodeEditor, JSONViewer } from '@gopherex/backplane-editors';
import { CircleX, OctagonX, Send } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, enumLabel } from './format.js';

export const runTone: Record<api.RunStatus, StatusTone> = {
  [api.RunStatus.UNSPECIFIED]: 'neutral', [api.RunStatus.RUNNING]: 'info', [api.RunStatus.COMPLETED]: 'success', [api.RunStatus.FAILED]: 'danger',
  [api.RunStatus.CANCELED]: 'neutral', [api.RunStatus.TERMINATED]: 'warning', [api.RunStatus.CONTINUED_AS_NEW]: 'neutral', [api.RunStatus.TIMED_OUT]: 'danger',
};
export function RunStatusBadge({ status }: { status: api.RunStatus }) {
  return <StatusBadge tone={runTone[status]}>{enumLabel(api.RunStatus, status)}</StatusBadge>;
}
export const runStatuses = [api.RunStatus.RUNNING, api.RunStatus.COMPLETED, api.RunStatus.FAILED, api.RunStatus.CANCELED, api.RunStatus.TERMINATED, api.RunStatus.TIMED_OUT];

/** Status filter chips shared by every run list. */
export function RunStatusFilter({ value, onChange }: { value: api.RunStatus; onChange: (status: api.RunStatus) => void }) {
  const text = usePlatformText();
  return <div className="flex flex-wrap gap-1" role="group" aria-label={text('status')}>
    {[api.RunStatus.UNSPECIFIED, ...runStatuses].map((status) => <button key={status} type="button" aria-pressed={value === status} onClick={() => onChange(status)}
      className="h-6 rounded-sm border border-border px-2 text-xs text-muted-foreground hover:text-foreground aria-pressed:border-primary/50 aria-pressed:bg-primary/10 aria-pressed:text-foreground">
      {status === api.RunStatus.UNSPECIFIED ? text('allStatuses') : enumLabel(api.RunStatus, status)}
    </button>)}
  </div>;
}

/** Dense run table; a row opens the run. */
export function RunTable({ runs, onOpen, loading, empty }: { runs: api.Run[]; onOpen: (run: api.Run) => void; loading?: boolean; empty?: ReactNode }) {
  const text = usePlatformText();
  if (loading && !runs.length) return <div className="grid gap-2 p-3">{[0, 1, 2].map((key) => <Skeleton key={key} className="h-8" />)}</div>;
  if (!runs.length) return <>{empty ?? <EmptyState className="py-8" title={text('noRuns')} />}</>;
  return <table aria-label={text('runs')} className="w-full text-sm">
    <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
      {[text('status'), text('workflow'), text('runId'), text('startedAt'), text('duration'), text('history')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}
    </tr></thead>
    <tbody>{runs.map((run) => <tr key={`${run.workflowId}/${run.runId}`} tabIndex={0} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none"
      onClick={() => onOpen(run)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(run); } }}>
      <td className="h-10 px-3"><RunStatusBadge status={run.status} /></td>
      <td className="max-w-80 px-3"><div className="truncate font-mono text-xs" title={run.workflowId}>{run.workflowId}</div><div className="truncate text-2xs text-muted-foreground">{run.workflowType}</div></td>
      <td className="px-3 font-mono text-2xs text-muted-foreground" title={run.runId}>{run.runId.slice(0, 8)}</td>
      <td className="px-3 text-xs text-muted-foreground"><Timestamp value={date(run.startTime)} /></td>
      <td className="px-3 text-xs">{run.startTime ? run.closeTime ? <Duration milliseconds={date(run.closeTime)!.getTime() - date(run.startTime)!.getTime()} /> : <Duration since={date(run.startTime)} className="text-info" /> : '—'}</td>
      <td className="px-3 font-mono text-xs text-muted-foreground">{run.historyLength.toString()}</td>
    </tr>)}</tbody>
  </table>;
}

/** Run detail in a drawer: summary, input/result, failure, pending activities, history, and run controls. */
/** Hooks for runs owned by another service API (binding and rule runs). */
export interface RunSource { load?: (run: { workflowId: string; runId: string }, signal: AbortSignal) => Promise<api.GetRunResponse>; cancel?: (run: { workflowId: string; runId: string }, signal: AbortSignal) => Promise<unknown> }
export function RunDrawer({ run, onClose, mode, children, source }: { run?: { workflowId: string; runId: string }; onClose: () => void; mode: 'dark' | 'light'; children?: ReactNode; source?: RunSource }) {
  const text = usePlatformText();
  return <DetailDrawer open={!!run} onOpenChange={(open) => { if (!open) onClose(); }} size="lg" title={run?.workflowId} description={run && <span className="font-mono">{text('runId')} {run.runId}</span>}>
    {run && <RunDetail key={`${run.workflowId}/${run.runId}`} workflowId={run.workflowId} runId={run.runId} mode={mode} source={source}>{children}</RunDetail>}
  </DetailDrawer>;
}

function RunDetail({ workflowId, runId, mode, children, source }: { workflowId: string; runId: string; mode: 'dark' | 'light'; children?: ReactNode; source?: RunSource }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const state = usePlatformQuery(`run:${workflowId}:${runId}`, (signal) => source?.load ? source.load({ workflowId, runId }, signal) : client.getRun(create(api.GetRunRequestSchema, { workflowId, runId }), { signal }));
  const [reason, setReason] = useState(''), [signalName, setSignalName] = useState(''), [signalInput, setSignalInput] = useState('{}');
  const detail = state.value, run = detail?.run;
  if (!detail) return state.error !== undefined ? <EmptyState icon={<CircleX />} title={text('error')} action={<Button size="sm" variant="outline" onClick={state.refresh}>{text('retry')}</Button>} /> : <div className="grid gap-2"><Skeleton className="h-24" /><Skeleton className="h-48" /></div>;
  const running = run?.status === api.RunStatus.RUNNING;
  const control = (execute: (signal: AbortSignal) => Promise<unknown>) => async (signal: AbortSignal) => {
    const result = await action.run((abort) => execute(AbortSignal.any([signal, abort])));
    if (result === undefined) throw new Error('Run control outcome is unavailable'); state.refresh();
  };
  return <div className="grid gap-4">
    {run && <KeyValueList items={[
      { label: text('status'), value: <RunStatusBadge status={run.status} /> },
      { label: text('workflow'), value: run.workflowType, mono: true }, { label: text('taskQueue'), value: run.taskQueue, mono: true },
      { label: text('startedAt'), value: <Timestamp value={date(run.startTime)} absolute /> },
      { label: text('closedAt'), value: run.closeTime ? <Timestamp value={date(run.closeTime)} absolute /> : <span className="text-info">{text('stillRunning')}</span> },
      ...(run.parentWorkflowId ? [{ label: text('parent'), value: run.parentWorkflowId, mono: true }] : []),
    ]} />}
    {detail.failure && <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive" role="alert"><div className="font-medium">{detail.failureType || text('failure')}</div><pre className="m-0 mt-1 font-mono text-xs whitespace-pre-wrap">{detail.failure}</pre></div>}
    {children}
    <div className="grid gap-3 lg:grid-cols-2">
      <div className="min-w-0"><div className="mb-1.5 text-xs text-muted-foreground">{text('input')}</div><JSONViewer label={text('input')} value={detail.input || 'null'} mode={mode} /></div>
      <div className="min-w-0"><div className="mb-1.5 text-xs text-muted-foreground">{text('output')}</div><JSONViewer label={text('output')} value={detail.result || 'null'} mode={mode} /></div>
    </div>
    {detail.pendingActivities.length > 0 && <Panel title={text('pendingActivities')} count={detail.pendingActivities.length} flush>
      {detail.pendingActivities.map((activity) => <div key={activity.activityId} className="grid gap-0.5 border-b border-border px-3 py-2 text-xs last:border-b-0">
        <div className="flex items-center gap-2"><span className="font-mono">{activity.activityType}</span><Badge variant="outline">{activity.state.toLowerCase()}</Badge><span className="ml-auto text-muted-foreground">{text('attempt', { attempt: activity.attempt, max: activity.maximumAttempts || '∞' })}</span></div>
        {activity.lastFailure && <div className="text-destructive">{activity.lastFailure}</div>}
      </div>)}
    </Panel>}
    <Panel title={text('history')} count={detail.history.length} flush footer={detail.truncated ? text('partial') : undefined}>
      <ol className="m-0 max-h-96 list-none overflow-y-auto p-0">{detail.history.map((event) => <li key={event.id.toString()} className="grid grid-cols-[40px_minmax(0,1fr)_auto] items-start gap-2 border-b border-border px-3 py-1.5 text-xs last:border-b-0">
        <span className="font-mono text-muted-foreground">{event.id.toString()}</span>
        <span className="min-w-0"><span className={`font-medium ${event.failure ? 'text-destructive' : ''}`}>{event.type}</span>{event.summary && <span className="ml-2 text-muted-foreground">{event.summary}</span>}
          {event.failure && <span className="block text-destructive">{event.failure}</span>}</span>
        <span className="text-muted-foreground"><Timestamp value={date(event.time)} /></span>
      </li>)}</ol>
    </Panel>
    {running && <Panel title={text('controls')}>
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <ConfirmAction trigger={<><CircleX className="size-3.5" />{text('cancel')}</>} title={text('cancelConfirm')} description={text('cancelHelp')} disabled={action.pending || action.disabled}
            onConfirm={control((signal) => source?.cancel ? source.cancel({ workflowId, runId }, signal) : client.cancelRun(create(api.CancelRunRequestSchema, { workflowId, runId }), { signal }))} />
          <Input className="h-8 w-56" aria-label={text('reason')} placeholder={text('terminateReason')} value={reason} onChange={(event) => setReason(event.target.value)} />
          <ConfirmAction trigger={<><OctagonX className="size-3.5" />{text('terminate')}</>} title={text('terminateConfirm')} description={text('terminateHelp')} disabled={action.pending || action.disabled}
            onConfirm={control((signal) => client.terminateRun(create(api.TerminateRunRequestSchema, { workflowId, runId, reason }), { signal }))} />
        </div>
        <div className="grid gap-2">
          <Input className="h-8" aria-label={text('signalName')} placeholder={text('signalName')} value={signalName} onChange={(event) => setSignalName(event.target.value)} />
          <CodeEditor label={text('signalInput')} value={signalInput} onChange={setSignalInput} language="json" mode={mode} height={80} />
          <div><ConfirmAction trigger={<><Send className="size-3.5" />{text('signalRun')}</>} title={text('signalConfirm')} description={signalName} disabled={!signalName || action.pending || action.disabled}
            onConfirm={control((signal) => client.signalRun(create(api.SignalRunRequestSchema, { workflowId, runId, signal: signalName, input: signalInput }), { signal }))} /></div>
        </div>
        {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
      </div>
    </Panel>}
  </div>;
}
