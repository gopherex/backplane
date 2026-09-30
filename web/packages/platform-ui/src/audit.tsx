import { mergeAudit } from './audit-model.js';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { AuditServiceClient, AuditFilterSchema, AuditEntrySchema, CatalogServiceClient, ListAuditRequestSchema, ListServicesRequestSchema, WatchAuditRequestSchema, type AuditEntry, type AuditFilter } from '@gopherex/backplane-api';
import { useBackplane, useClient, useConnection } from '@gopherex/backplane-react';
import { watchWithRetry, WsStatusError } from '@gopherex/backplane-client';
import { Button, Count, DetailDrawer, EmptyState, FilterCombo, KeyValueList, Panel, Skeleton, StatusBadge, StatusDot, Timestamp, type StatusTone } from '@gopherex/backplane-ui';
import { ClipboardList, Filter, RefreshCw, TriangleAlert, Workflow, X } from 'lucide-react';
import { JSONViewer } from '@gopherex/backplane-editors';
import { usePlatformText } from './locales.js';
import { RunDrawer, type RunRef } from './runs.js';

export function useAuditFeed(filter: AuditFilter, maxEntries = 1000) {
  const owner = useBackplane(), client = useClient(AuditServiceClient), connection = useConnection(), session = connection.session?.id ?? null;
  const key = toJsonString(AuditFilterSchema, filter), limit = Math.max(100, Math.min(10000, maxEntries));
  const [reload, setReload] = useState(0), [page, setPage] = useState('');
  const cache = useRef<{ owner: typeof owner; session: typeof session; key: string; reload: number; cursor: string; next: string; entries: AuditEntry[]; bounded: boolean } | undefined>(undefined);
  const [version, setVersion] = useState(0), [status, setStatus] = useState<'loading' | 'ready' | 'stale' | 'error' | 'gap'>('loading');
  useEffect(() => {
    const controller = new AbortController();
    if (cache.current?.owner !== owner || cache.current.session !== session || cache.current.key !== key || cache.current.reload !== reload) {
      cache.current = { owner, session, key, reload, cursor: '', next: '', entries: [], bounded: false }; setStatus('loading'); setPage('');
    }
    const current = cache.current;
    if (connection.connection !== 'connected') { setStatus(current.entries.length ? 'stale' : 'loading'); return () => controller.abort(); }
    void (async () => {
      try {
        if (!current.cursor) {
          const result = await client.listAudit(create(ListAuditRequestSchema, { filter, pageSize: 100 }), { signal: controller.signal });
          if (controller.signal.aborted) return;
          current.entries = result.entries; current.next = result.nextPageCursor; current.cursor = result.watchCursor; setVersion((value) => value + 1);
        }
        setStatus('ready');
        for await (const result of watchWithRetry((signal) => client.watchAudit(create(WatchAuditRequestSchema, { filter, afterCursor: current.cursor }), { signal }), {
          signal: controller.signal, onRetry: () => { if (!controller.signal.aborted) setStatus('stale'); },
        })) {
          if (controller.signal.aborted) return;
          current.bounded ||= new Set([...current.entries, ...result.entries].map((entry) => entry.id)).size > limit;
          current.entries = mergeAudit(current.entries, result.entries, limit); current.cursor = result.cursor; setStatus('ready'); setVersion((value) => value + 1);
        }
      } catch (error) { if (!controller.signal.aborted) setStatus(error instanceof WsStatusError && error.code === 11 ? 'gap' : 'error'); }
    })();
    return () => controller.abort();
  }, [owner, client, session, key, reload, limit, connection.connection, connection.connectionId]);
  useEffect(() => {
    if (!page || connection.connection !== 'connected') return;
    const controller = new AbortController(), current = cache.current;
    void client.listAudit(create(ListAuditRequestSchema, { filter, pageSize: 100, pageCursor: page }), { signal: controller.signal }).then((result) => {
      if (controller.signal.aborted || !current || cache.current !== current) return;
      current.bounded ||= new Set([...current.entries, ...result.entries].map((entry) => entry.id)).size > limit;
      current.entries = mergeAudit(current.entries, result.entries, limit); current.next = result.nextPageCursor; setPage(''); setVersion((value) => value + 1);
    }, () => { if (!controller.signal.aborted) { setStatus('error'); setPage(''); } });
    return () => controller.abort();
  }, [page, key, owner, client, session, connection.connection, connection.connectionId, limit]);
  const visible = cache.current?.owner === owner && cache.current.session === session && cache.current.key === key && cache.current.reload === reload ? cache.current : undefined;
  void version;
  return { entries: visible?.entries ?? [], status, bounded: visible?.bounded ?? false, next: visible?.next ?? '', paging: !!page,
    more: () => setPage(visible?.next ?? ''), refresh: () => setReload((value) => value + 1) };
}

const outcomeTone = (outcome: string): StatusTone => outcome === 'succeeded' ? 'success' : outcome === 'failed' || outcome === 'rejected' ? 'danger' : outcome === 'partial' ? 'warning' : outcome === 'intent' ? 'info' : 'neutral';
const outcomes = ['succeeded', 'failed', 'rejected', 'partial', 'intent', 'unknown'];
// Every action the platform records (internal/audit and control mutations).
const actions = ['config.save', 'config.rollback', 'binding.save', 'binding.delete', 'binding.rollback', 'binding.test', 'binding.cancel',
  'rule.save', 'rule.delete', 'rule.rollback', 'rule.pause', 'rule.test', 'rule.cancel', 'hook.call', 'activity.run',
  'event.publish_test', 'event.redrive', 'event.purge', 'workflow.start', 'workflow.cancel', 'workflow.terminate', 'workflow.signal',
  'schedule.pause', 'schedule.resume', 'schedule.trigger', 'session.create', 'session.revoke', 'session.revoke_others', 'session.expire'];
const distinct = (values: string[]) => [...new Set(values.filter(Boolean))].sort();
type FilterKey = 'actor' | 'action' | 'subject' | 'outcome' | 'operationId' | 'service';
type Filters = Partial<Record<FilterKey, string>>;

/** Durable audit trail with filters, live tail and an entry drawer. */
export function AuditFeed({ initialFilter, service, mode }: { initialFilter?: Partial<Omit<AuditFilter, '$typeName' | '$unknown'>>; service?: string; mode: 'dark' | 'light' }) {
  const text = usePlatformText();
  const [draft, setDraft] = useState<Filters>(initialFilter ?? {}), [filters, setFilters] = useState<Filters>(initialFilter ?? {}), [selected, setSelected] = useState<string>(), [run, setRun] = useState<RunRef>();
  // Typing narrows after a pause; every applied filter starts a new snapshot and watch.
  useEffect(() => { const timer = setTimeout(() => setFilters(draft), 400); return () => clearTimeout(timer); }, [draft]);
  const filter = useMemo(() => create(AuditFilterSchema, { ...filters, service: service ?? filters.service ?? '' }), [filters, service]), state = useAuditFeed(filter);
  const catalog = useClient(CatalogServiceClient);
  const loadServices = useCallback(async (signal: AbortSignal) => (await catalog.listServices(create(ListServicesRequestSchema), { signal })).services.map((entry) => entry.name), [catalog]);
  const entry = state.entries.find((item) => item.id === selected);
  const apply = (patch: Filters) => { const next = { ...draft, ...patch }; setDraft(next); setFilters(next); };
  const active = Object.entries(filters).filter(([, value]) => value);
  const live = state.status === 'ready';
  return <section aria-label={text('audit')} className="flex h-full min-h-0 flex-col gap-3">
    <div className="flex shrink-0 flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2">
      <Filter className="ml-1 size-4 text-muted-foreground" aria-hidden="true" />
      {service && <span className="inline-flex h-8 items-center rounded-md border border-border bg-raised px-2.5 font-mono text-xs">{text('serviceScope', { service })}</span>}
      <FilterCombo mono label={text('action')} value={draft.action ?? ''} onChange={(action) => apply({ action })} options={distinct([...actions, ...state.entries.map((item) => item.action)])} />
      <FilterCombo label={text('outcome')} value={draft.outcome ?? ''} onChange={(outcome) => apply({ outcome })} options={outcomes} allowCustom={false} />
      {!service && <FilterCombo mono label={text('service')} value={draft.service ?? ''} onChange={(value) => apply({ service: value })} loadOptions={loadServices} options={distinct(state.entries.map((item) => item.service))} />}
      <FilterCombo mono label={text('actor')} value={draft.actor ?? ''} onChange={(actor) => apply({ actor })} options={distinct(state.entries.map((item) => item.actor))} />
      <FilterCombo mono label={text('subject')} value={draft.subject ?? ''} onChange={(subject) => apply({ subject })} options={distinct(state.entries.map((item) => item.subject))} />
      <FilterCombo mono label={text('operation')} value={draft.operationId ?? ''} onChange={(operationId) => apply({ operationId })} options={distinct(state.entries.map((item) => item.operationId)).slice(0, 50)} />
      {active.length > 0 && <Button size="sm" variant="ghost" onClick={() => { setDraft({}); setFilters({}); }}><X />{text('clearFilters')}</Button>}
      <span className="ml-auto inline-flex items-center gap-2 text-xs text-muted-foreground" role="status">
        <StatusDot tone={live ? 'success' : state.status === 'gap' || state.status === 'error' ? 'danger' : 'warning'} pulse={live} />{text(live ? 'liveTail' : state.status === 'gap' ? 'gapShort' : state.status === 'error' ? 'error' : state.status)}
      </span>
      <Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh}><RefreshCw /></Button>
    </div>
    {state.status === 'gap' && <div className="flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning" role="alert"><TriangleAlert className="size-4" />{text('gap')}<Button size="xs" variant="outline" className="ml-auto" onClick={state.refresh}>{text('reload')}</Button></div>}
    {state.bounded && <p className="m-0 text-xs text-muted-foreground" role="status">{text('bounded')}</p>}
    <Panel fill title={<><ClipboardList className="size-4 text-muted-foreground" />{text('entries')}</>} count={state.entries.length} flush
      footer={<><span>{text('newestFirst')}</span><Button size="xs" variant="ghost" className="ml-auto" disabled={!state.next || state.paging || state.bounded} onClick={state.more}>{state.paging ? text('loading') : text('more')}</Button></>}>
      {state.status === 'loading' && !state.entries.length ? <div className="grid gap-1 p-3">{[0, 1, 2, 3, 4].map((key) => <Skeleton key={key} className="h-7" />)}</div>
        : !state.entries.length ? <EmptyState className="py-10" icon={<ClipboardList />} title={text('noEntries')} description={active.length ? text('noEntriesFiltered') : undefined} />
        : <table aria-label={text('audit')} className="w-full text-sm">
          <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">{[text('time'), ...(service ? [] : [text('service')]), text('actor'), text('action'), text('subject'), text('outcome'), text('operation')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}</tr></thead>
          <tbody>{state.entries.map((item) => <tr key={item.id} tabIndex={0} aria-selected={item.id === selected} onClick={() => setSelected(item.id)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); setSelected(item.id); } }}
            className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10">
            <td className="h-9 px-3 text-xs whitespace-nowrap text-muted-foreground"><span className="mr-2 font-mono text-2xs text-subtle">#{item.sequence.toString()}</span><Timestamp value={item.createdAt ? timestampDate(item.createdAt) : undefined} /></td>
            {!service && <td className="px-3 font-mono text-xs">{item.service || <span className="text-subtle">—</span>}</td>}
            <td className="max-w-48 truncate px-3 font-mono text-xs" title={item.actor}>{item.actor}</td>
            <td className="px-3"><span className="rounded-sm bg-raised px-1.5 py-0.5 font-mono text-xs">{item.action}</span></td>
            <td className="max-w-72 truncate px-3 font-mono text-xs" title={item.subject}>{item.subject}</td>
            <td className="px-3"><StatusBadge tone={outcomeTone(item.outcome)}>{item.outcome || 'unknown'}</StatusBadge></td>
            <td className="px-3"><button type="button" className="font-mono text-2xs text-muted-foreground hover:text-link" title={text('filterOperation')} onClick={(event) => { event.stopPropagation(); apply({ operationId: item.operationId }); }}>{item.operationId.slice(0, 8)}</button></td>
          </tr>)}</tbody>
        </table>}
    </Panel>
    <DetailDrawer open={!!entry} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg" title={entry && <span className="font-mono">{entry.action}</span>} description={entry && <span className="font-mono">#{entry.sequence.toString()} · {entry.subject}</span>}
      actions={entry && <><Button size="xs" variant="outline" onClick={() => { apply({ operationId: entry.operationId }); setSelected(undefined); }}><Filter />{text('filterOperation')}</Button>
        {entry.detail?.workflowId && <Button size="xs" variant="outline" onClick={() => { setRun({ workflowId: entry.detail!.workflowId, runId: entry.detail!.runId }); setSelected(undefined); }}><Workflow />{text('openRun')}</Button>}</>}>
      {entry && <div className="grid gap-4">
        <KeyValueList items={[
          { label: text('outcome'), value: <StatusBadge tone={outcomeTone(entry.outcome)}>{entry.outcome || 'unknown'}</StatusBadge> },
          { label: text('time'), value: <Timestamp value={entry.createdAt ? timestampDate(entry.createdAt) : undefined} absolute /> },
          { label: text('service'), value: entry.service || '—', mono: true }, { label: text('actor'), value: entry.actor, mono: true }, { label: text('subject'), value: entry.subject, mono: true },
          { label: text('operation'), value: entry.operationId, mono: true }, { label: text('entryId'), value: entry.id, mono: true },
        ]} />
        {entry.outcome === 'intent' && <p className="m-0 rounded-md border border-info/30 bg-info/10 px-3 py-2 text-xs text-info">{text('intentHelp')}</p>}
        <div><div className="mb-1.5 flex items-center gap-2 text-xs text-muted-foreground">{text('detail')}<Count>JSON</Count></div>
          <JSONViewer label={text('detail')} value={toJsonString(AuditEntrySchema, entry, { prettySpaces: 2 })} mode={mode} height={360} /></div>
      </div>}
    </DetailDrawer>
    <RunDrawer run={run} onClose={() => setRun(undefined)} mode={mode} />
  </section>;
}
