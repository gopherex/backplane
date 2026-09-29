import { mergeAudit } from './audit-model.js';
import { useEffect, useMemo, useRef, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { AuditServiceClient, AuditFilterSchema, AuditEntrySchema, ListAuditRequestSchema, WatchAuditRequestSchema, type AuditEntry, type AuditFilter } from '@gopherex/backplane-api';
import { useBackplane, useClient, useConnection } from '@gopherex/backplane-react';
import { watchWithRetry, WsStatusError } from '@gopherex/backplane-client';
import { Button, DataTable, Input } from '@gopherex/backplane-ui';
import { JSONViewer } from '@gopherex/backplane-editors';
import { ConnectionNotice } from './runtime.js';
import { usePlatformText } from './locales.js';

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

export function AuditFeed({ initialFilter, mode }: { initialFilter?: Partial<Omit<AuditFilter, '$typeName' | '$unknown'>>; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), [filters, setFilters] = useState(initialFilter ?? {}), [selected, setSelected] = useState<string>();
  const filter = useMemo(() => create(AuditFilterSchema, filters), [filters]), state = useAuditFeed(filter);
  const entry = state.entries.find((entry) => entry.id === selected);
  return <section aria-label={text('audit')} style={{ display: 'grid', gap: 12 }}><ConnectionNotice />
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(180px,1fr))', gap: 8 }}>{(['actor', 'action', 'subject', 'outcome', 'operationId'] as const).map((key) => <Input key={key} aria-label={text(key === 'operationId' ? 'operation' : key)} value={filters[key] ?? ''} onChange={(event) => setFilters({ ...filters, [key]: event.target.value })} />)}</div>
    {state.status !== 'ready' && <p role="status">{text(state.status === 'gap' ? 'gap' : state.status === 'error' ? 'error' : state.status)}</p>}
    {state.bounded && <p role="status">{text('bounded')}</p>}
    <Button type="button" variant="outline" onClick={state.refresh}>{text('refresh')}</Button>
    <DataTable label={text('audit')} data={state.entries} getRowId={(entry) => entry.id} onActivate={(entry) => setSelected(entry.id)} columns={[
      { id: 'sequence', label: text('sequence'), value: (entry) => entry.sequence },
      { id: 'time', label: text('time'), value: (entry) => entry.createdAt ? timestampDate(entry.createdAt).toISOString() : '', width: 260 },
      { id: 'actor', label: text('actor'), value: (entry) => entry.actor }, { id: 'action', label: text('action'), value: (entry) => entry.action },
      { id: 'subject', label: text('subject'), value: (entry) => entry.subject }, { id: 'outcome', label: text('outcome'), value: (entry) => entry.outcome },
      { id: 'operation', label: text('operation'), value: (entry) => entry.operationId },
    ]} />
    <Button type="button" variant="outline" disabled={!state.next || state.paging || state.bounded} onClick={state.more}>{text('more')}</Button>
    {entry && <JSONViewer label={text('detail')} value={toJsonString(AuditEntrySchema, entry, { prettySpaces: 2 })} mode={mode} />}
  </section>;
}
