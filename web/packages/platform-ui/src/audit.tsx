import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { create, toJson, type JsonValue } from '@bufbuild/protobuf';
import { timestampDate, ValueSchema, type Value } from '@bufbuild/protobuf/wkt';
import { AuditHistogramRequestSchema, AuditFacetsRequestSchema, AuditFieldsRequestSchema, AuditServiceClient, AuditSource, SearchAuditRequestSchema,
  type AuditFacet, type AuditRecord } from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { BarChart } from '@gopherex/backplane-charts';
import { Badge, Button, Combobox, Count, DetailDrawer, EmptyState, Input, NativeSelect, Panel, Popover, PopoverContent, PopoverTrigger,
  RefreshControl, Skeleton, StatusBadge, TimeRangeControl, Timestamp, resolveTimeRange, type TimeRangeValue } from '@gopherex/backplane-ui';
import { JSONViewer } from '@gopherex/backplane-editors';
import { Boxes, ClipboardList, Minus, Plus, Search, ShieldCheck, Waypoints, Workflow, X } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { usePlatformQuery } from './runtime.js';
import { RunDrawer, type RunRef } from './runs.js';
import { serviceColor } from './wiring/graph.js';
import { addValue, auditFields, auditOps, conditionOf, detailFacts, fieldOps, filterOf, humanActor, numeric, outcomeTone, parseValue, sameTarget, takesValues,
  targetKey, type AuditChip, type AuditFieldName, type AuditOp, type AuditTarget } from './audit-model.js';

/** What the audit page shows; the host keeps it in the URL. */
export interface AuditState { range: TimeRangeValue; chips: AuditChip[]; text: string; /** Refresh interval, ms; 0: off. */ live: number }
export const defaultAuditState: AuditState = { range: { from: 'now-24h', to: 'now', timeZone: 'browser' }, chips: [], text: '', live: 10_000 };

const facetFields: readonly AuditFieldName[] = ['source', 'service', 'action', 'outcome', 'actor'];
const hidden = (key: string) => key === 'backplane.audit' || key === 'backplane.ingest';
const jsonOf = (value?: Value): JsonValue => value ? toJson(ValueSchema, value) : null;
const label = (value: JsonValue) => typeof value === 'string' ? value : JSON.stringify(value);

/**
 * The audit feed: platform control entries and application records in one
 * table, narrowed by conditions on fields and attributes, text and a time
 * range; a histogram over the range, value counts per field, a live refresh
 * and a record drawer. `service` pins a service condition.
 */
export function AuditExplorer({ mode, service, state, onStateChange, onOpenTrace }: {
  mode: 'dark' | 'light'; service?: string; state: AuditState; onStateChange: (state: AuditState) => void; onOpenTrace?: (traceId: string) => void;
}) {
  const text = usePlatformText(), client = useClient(AuditServiceClient);
  const [tick, setTick] = useState(0), [pages, setPages] = useState<string[]>([]), [selected, setSelected] = useState<string>(), [run, setRun] = useState<RunRef>();
  const [draftText, setDraftText] = useState(state.text);
  useEffect(() => setDraftText(state.text), [state.text]);
  useEffect(() => { if (draftText === state.text) return; const timer = setTimeout(() => onStateChange({ ...state, text: draftText }), 400); return () => clearTimeout(timer); }, [draftText]); // eslint-disable-line react-hooks/exhaustive-deps
  const chips = useMemo(() => service ? [{ target: { field: 'service' as const }, op: 'is' as const, values: [service] }, ...state.chips] : state.chips, [service, state.chips]);
  // The range resolves again on every refresh: "now-24h" moves with now.
  const range = useMemo(() => resolveTimeRange(state.range), [state.range, tick]); // eslint-disable-line react-hooks/exhaustive-deps
  const filter = useMemo(() => filterOf(chips, state.text, range), [chips, state.text, range]);
  const key = JSON.stringify([chips, state.text, state.range]);
  useEffect(() => setPages([]), [key]);
  const setChips = (next: AuditChip[]) => onStateChange({ ...state, chips: service ? next.filter((chip) => !(sameTarget(chip.target, { field: 'service' }) && chip.op === 'is' && chip.values.length === 1 && chip.values[0] === service)) : next });
  const add = (target: AuditTarget, value: JsonValue, exclude = false) => setChips(addValue(state.chips, target, value, exclude));

  const feed = usePlatformQuery(`audit-feed:${key}:${tick}:${pages.join(',')}`, async (signal) => {
    const records: AuditRecord[] = [];
    let next = '';
    for (const cursor of ['', ...pages]) {
      const page = await client.searchAudit(create(SearchAuditRequestSchema, { filter, pageSize: 100, pageCursor: cursor }), { signal });
      records.push(...page.records); next = page.nextPageCursor;
    }
    return { records, next };
  });
  const histogram = usePlatformQuery(`audit-histogram:${key}:${tick}`, (signal) => client.auditHistogram(create(AuditHistogramRequestSchema, { filter, buckets: 60 }), { signal }));
  const fields = usePlatformQuery(`audit-fields:${key}:${tick}`, (signal) => client.auditFields(create(AuditFieldsRequestSchema, { filter }), { signal }));
  const attributeKeys = useMemo(() => (fields.value?.fields ?? []).filter((field) => !hidden(field.attribute)), [fields.value]);
  const facetTargets = useMemo<AuditTarget[]>(() => [...facetFields.map((field) => ({ field })), ...attributeKeys.slice(0, 6).map((field) => ({ attribute: field.attribute }))], [attributeKeys]);
  const facets = usePlatformQuery(`audit-facets:${key}:${tick}:${facetTargets.map(targetKey).join(',')}`, (signal) =>
    client.auditFacets(create(AuditFacetsRequestSchema, { filter, targets: facetTargets.map(conditionOf), limit: 8 }), { signal }));

  // Records that appeared since the page was open light up once.
  const seen = useRef<Set<string> | undefined>(undefined), [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  const records = feed.value?.records ?? [];
  useEffect(() => { seen.current = undefined; }, [key]);
  useEffect(() => {
    if (!feed.value) return;
    const ids = feed.value.records.map((record) => record.id);
    if (seen.current) { const added = ids.filter((id) => !seen.current!.has(id)); if (added.length) { setFresh(new Set(added)); setTimeout(() => setFresh(new Set()), 2500); } }
    seen.current = new Set([...(seen.current ?? []), ...ids]);
  }, [feed.value]);
  const refresh = useCallback(async () => { setTick((value) => value + 1); }, []);
  const record = records.find((item) => item.id === selected);

  return <section aria-label={text('audit')} className="flex h-full min-h-0 flex-col gap-3">
    <div className="flex shrink-0 flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2">
      {service && <span className="inline-flex h-7 items-center rounded-md border border-border bg-raised px-2 font-mono text-xs">{text('serviceScope', { service })}</span>}
      {state.chips.map((chip, index) => <ChipEditor key={index} chip={chip} attributes={attributeKeys.map((field) => field.attribute)} facets={facets.value?.facets}
        onChange={(next) => setChips(state.chips.map((other, at) => at === index ? next : other))} onRemove={() => setChips(state.chips.filter((_, at) => at !== index))} />)}
      <ChipEditor attributes={attributeKeys.map((field) => field.attribute)} facets={facets.value?.facets} onChange={(next) => setChips([...state.chips, next])} />
      <span className="relative ml-auto"><Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-muted-foreground" />
        <Input className="h-8 w-56 pl-7 text-xs" aria-label={text('searchText')} placeholder={text('searchText')} value={draftText} onChange={(event) => setDraftText(event.target.value)} /></span>
      {(state.chips.length > 0 || state.text) && <Button size="sm" variant="ghost" onClick={() => onStateChange({ ...state, chips: [], text: '' })}><X />{text('clearFilters')}</Button>}
    </div>
    <div className="flex shrink-0 flex-wrap items-center gap-3">
      <TimeRangeControl value={state.range} onChange={(value) => onStateChange({ ...state, range: value })} mode={mode} label={text('timeRange')} />
      <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">{text('liveRefresh')}
        <RefreshControl interval={state.live} onIntervalChange={(live) => onStateChange({ ...state, live })} onRefresh={refresh} /></span>
    </div>
    <Panel className="shrink-0" title={text('auditOverTime')} count={histogram.value?.buckets.reduce((sum, bucket) => sum + Number(bucket.platform + bucket.application), 0)}>
      {histogram.value && histogram.value.buckets.length > 0
        ? <BarChart label={text('auditOverTime')} mode={mode} height={110} controls={false} series={[
            { id: 'platform', label: text('platformAudit'), color: 'var(--chart-2)', points: histogram.value.buckets.map((bucket) => ({ x: bucket.start ? timestampDate(bucket.start).getTime() : 0, value: bucket.platform })) },
            { id: 'application', label: text('applicationAudit'), color: 'var(--chart-1)', points: histogram.value.buckets.map((bucket) => ({ x: bucket.start ? timestampDate(bucket.start).getTime() : 0, value: bucket.application })) },
            { id: 'failed', label: text('failedRecords'), color: 'var(--destructive)', points: histogram.value.buckets.map((bucket) => ({ x: bucket.start ? timestampDate(bucket.start).getTime() : 0, value: bucket.failed })) },
          ]} onRangeChange={({ from, to }) => onStateChange({ ...state, range: { ...state.range, from: new Date(from).toISOString(), to: new Date(to).toISOString() } })} />
        : <div className="grid h-[110px] place-items-center text-xs text-muted-foreground">{histogram.loading ? <Skeleton className="h-full w-full" /> : text('noEntries')}</div>}
    </Panel>
    <div className="grid min-h-0 flex-1 grid-cols-[240px_minmax(0,1fr)] gap-3">
      <Panel fill flush title={text('auditFields')} className="min-h-0">
        <div className="grid gap-3 p-2">{(facets.value?.facets ?? []).filter((facet) => facet.values.length).map((facet) => <FacetList key={facetTitle(facet)} facet={facet} chips={chips} onAdd={add} />)}</div>
      </Panel>
      <Panel fill flush title={<><ClipboardList className="size-4 text-muted-foreground" />{text('entries')}</>} count={records.length}
        footer={<><span>{text('newestFirst')}</span><Button size="xs" variant="ghost" className="ml-auto" disabled={!feed.value?.next || feed.loading} onClick={() => setPages([...pages, feed.value!.next])}>{feed.loading && pages.length ? text('loading') : text('more')}</Button></>}>
        {feed.loading && !records.length ? <div className="grid gap-1 p-3">{[0, 1, 2, 3, 4, 5].map((at) => <Skeleton key={at} className="h-8" />)}</div>
          : !records.length ? <EmptyState className="py-10" icon={<ClipboardList />} title={text('noEntries')} description={chips.length || state.text ? text('noEntriesFiltered') : text('auditEmptyHelp')} />
          : <table aria-label={text('audit')} className="w-full text-sm">
            <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
              {[text('time'), '', text('service'), text('action'), text('actor'), text('subject'), text('outcome'), text('detail'), ''].map((heading, at) => <th key={at} className="h-8 px-2 font-medium whitespace-nowrap">{heading}</th>)}</tr></thead>
            <tbody>{records.map((item) => <Row key={item.id} record={item} fresh={fresh.has(item.id)} selected={item.id === selected} onOpen={() => setSelected(item.id)}
              onAdd={add} onTrace={onOpenTrace} onRun={setRun} />)}</tbody>
          </table>}
      </Panel>
    </div>
    <DetailDrawer open={!!record} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg" title={record && <span className="font-mono">{record.action}</span>}
      description={record && <span className="font-mono">{record.service || '—'}{record.subject && ` · ${record.subject}`}</span>}
      actions={record && <>
        {record.traceId && onOpenTrace && <Button size="xs" variant="outline" onClick={() => { onOpenTrace(record.traceId); setSelected(undefined); }}><Waypoints />{text('openTrace')}</Button>}
        {typeof record.attributes?.workflow_id === 'string' && <Button size="xs" variant="outline" onClick={() => { setRun({ workflowId: String(record.attributes!.workflow_id), runId: String(record.attributes!.run_id ?? '') }); setSelected(undefined); }}><Workflow />{text('openRun')}</Button>}
        {record.operationId && <Button size="xs" variant="outline" onClick={() => { add({ field: 'operation' }, record.operationId); setSelected(undefined); }}>{text('filterOperation')}</Button>}</>}>
      {record && <RecordDetail record={record} mode={mode} onAdd={(target, value, exclude) => add(target, value, exclude)} />}
    </DetailDrawer>
    <RunDrawer run={run} onClose={() => setRun(undefined)} mode={mode} />
  </section>;
}

function facetTitle(facet: AuditFacet): string {
  const target = facet.target?.target;
  return target?.case === 'attribute' ? target.value : auditFields[(target?.value as number ?? 1) - 1] ?? '';
}

function targetOf(facet: AuditFacet): AuditTarget {
  const target = facet.target?.target;
  return target?.case === 'attribute' ? { attribute: target.value } : { field: auditFields[(target?.value as number ?? 1) - 1]! };
}

/** One field's most frequent values: click to keep, Alt+click to exclude. */
function FacetList({ facet, chips, onAdd }: { facet: AuditFacet; chips: readonly AuditChip[]; onAdd: (target: AuditTarget, value: JsonValue, exclude?: boolean) => void }) {
  const text = usePlatformText(), target = targetOf(facet), title = facetTitle(facet);
  const active = (value: JsonValue) => chips.some((chip) => sameTarget(chip.target, target) && chip.op === 'is' && chip.values.some((v) => JSON.stringify(v) === JSON.stringify(value)));
  return <div className="grid gap-0.5">
    <div className="flex items-baseline gap-1 px-1 text-2xs font-medium tracking-wide text-muted-foreground uppercase">
      <span className={'field' in target ? '' : 'font-mono normal-case'}>{'field' in target ? text(`auditField_${target.field}`) : title}</span><span className="ml-auto font-normal">{facet.total.toString()}</span></div>
    {facet.values.map((entry) => { const value = jsonOf(entry.value), share = facet.total ? Number(entry.count) / Number(facet.total) : 0;
      return <button key={JSON.stringify(value)} type="button" title={text('facetHelp')} aria-pressed={active(value)} onClick={(event) => onAdd(target, value, event.altKey)}
        className="relative flex h-6 items-center gap-1.5 overflow-hidden rounded-sm px-1.5 text-left text-xs hover:bg-raised aria-pressed:bg-primary/10">
        <span className="absolute inset-y-0 left-0 bg-primary/10" style={{ width: `${Math.round(share * 100)}%` }} />
        {'field' in target && target.field === 'service' && <span className="relative size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(label(value)) }} />}
        <span className="relative min-w-0 flex-1 truncate font-mono">{'field' in target && target.field === 'actor' ? humanActor(label(value)) : label(value)}</span>
        <span className="relative text-2xs text-muted-foreground tabular-nums">{entry.count.toString()}</span>
      </button>; })}
  </div>;
}

function Row({ record, fresh, selected, onOpen, onAdd, onTrace, onRun }: {
  record: AuditRecord; fresh: boolean; selected: boolean; onOpen: () => void; onAdd: (target: AuditTarget, value: JsonValue, exclude?: boolean) => void;
  onTrace?: (traceId: string) => void; onRun: (run: RunRef) => void;
}) {
  const text = usePlatformText(), platform = record.source === AuditSource.PLATFORM, facts = detailFacts(record);
  const workflow = typeof record.attributes?.workflow_id === 'string' ? String(record.attributes.workflow_id) : '';
  const cell = (target: AuditTarget, value: string, children: ReactNode) => <button type="button" className="max-w-full truncate text-left hover:text-link" title={text('facetHelp')}
    onClick={(event) => { event.stopPropagation(); onAdd(target, value, event.altKey); }}>{children}</button>;
  return <tr tabIndex={0} aria-selected={selected} onClick={onOpen} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(); } }}
    className={`cursor-pointer border-b border-border transition-colors duration-1000 last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10 ${fresh ? 'bg-info/15' : ''}`}>
    <td className="h-9 px-2 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={record.time ? timestampDate(record.time) : undefined} /></td>
    <td className="px-1" title={text(platform ? 'platformAudit' : 'applicationAudit')}>{platform ? <ShieldCheck className="size-3.5 text-chart-2" aria-label={text('platformAudit')} /> : <Boxes className="size-3.5 text-chart-1" aria-label={text('applicationAudit')} />}</td>
    <td className="max-w-32 px-2 font-mono text-xs">{record.service ? cell({ field: 'service' }, record.service, <span className="inline-flex items-center gap-1.5"><span className="size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(record.service) }} />{record.service}</span>) : <span className="text-subtle">—</span>}</td>
    <td className="max-w-64 px-2">{cell({ field: 'action' }, record.action, <span className="rounded-sm bg-raised px-1.5 py-0.5 font-mono text-xs">{record.action}</span>)}</td>
    <td className="max-w-40 px-2 font-mono text-xs" title={record.actor}>{record.actor ? cell({ field: 'actor' }, record.actor, humanActor(record.actor)) : <span className="text-subtle">—</span>}</td>
    <td className="max-w-56 px-2 font-mono text-xs" title={record.subject}>{record.subject ? cell({ field: 'subject' }, record.subject, record.subject) : <span className="text-subtle">—</span>}</td>
    <td className="px-2">{record.outcome ? <StatusBadge tone={outcomeTone(record.outcome)}>{record.outcome}</StatusBadge> : <span className="text-subtle">—</span>}</td>
    <td className="max-w-96 truncate px-2 text-xs text-muted-foreground" title={facts.join(' · ')}>{facts.join(' · ') || '—'}</td>
    <td className="px-2 whitespace-nowrap">
      {record.traceId && onTrace && <Button size="icon-sm" variant="ghost" aria-label={text('openTrace')} title={text('openTrace')} onClick={(event) => { event.stopPropagation(); onTrace(record.traceId); }}><Waypoints /></Button>}
      {workflow && <Button size="icon-sm" variant="ghost" aria-label={text('openRun')} title={text('openRun')} onClick={(event) => { event.stopPropagation(); onRun({ workflowId: workflow, runId: String(record.attributes?.run_id ?? '') }); }}><Workflow /></Button>}
    </td>
  </tr>;
}

/** Every field and attribute of a record, each with keep/exclude filters. */
function RecordDetail({ record, mode, onAdd }: { record: AuditRecord; mode: 'dark' | 'light'; onAdd: (target: AuditTarget, value: JsonValue, exclude: boolean) => void }) {
  const text = usePlatformText();
  const fields: [AuditTarget, string, string][] = ([['source', record.source === AuditSource.PLATFORM ? 'platform' : 'application'], ['service', record.service], ['action', record.action],
    ['actor', record.actor], ['subject', record.subject], ['outcome', record.outcome], ['operation', record.operationId], ['severity', record.severity], ['trace_id', record.traceId]] as const)
    .filter(([, value]) => value).map(([field, value]) => [{ field }, text(`auditField_${field}`), value]);
  const attributes = Object.entries((record.attributes ?? {}) as Record<string, JsonValue>).sort(([a], [b]) => a.localeCompare(b));
  const row = (target: AuditTarget, name: ReactNode, value: JsonValue) => <tr key={targetKey(target)} className="border-b border-border last:border-b-0">
    <td className="py-1 pr-3 align-top text-xs whitespace-nowrap text-muted-foreground">{name}</td>
    <td className="py-1 font-mono text-xs break-all">{label(value)}</td>
    <td className="w-14 py-1 text-right whitespace-nowrap">
      <Button size="icon-sm" variant="ghost" aria-label={text('filterFor', { name: targetKey(target) })} title={text('filterFor', { name: targetKey(target) })} onClick={() => onAdd(target, value, false)}><Plus /></Button>
      <Button size="icon-sm" variant="ghost" aria-label={text('filterOut', { name: targetKey(target) })} title={text('filterOut', { name: targetKey(target) })} onClick={() => onAdd(target, value, true)}><Minus /></Button></td>
  </tr>;
  return <div className="grid gap-4">
    <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
      <span>{text('time')}: <Timestamp absolute value={record.time ? timestampDate(record.time) : undefined} className="text-foreground" /></span>
      <span>{text('receivedAt')}: <Timestamp absolute value={record.receivedAt ? timestampDate(record.receivedAt) : undefined} className="text-foreground" /></span>
      {record.sequence > 0n && <span>#{record.sequence.toString()}</span>}
    </div>
    {record.message && <p className="m-0 rounded-md border border-border bg-raised px-3 py-2 font-mono text-xs break-all">{record.message}</p>}
    <table className="w-full"><tbody>
      {fields.map(([target, name, value]) => row(target, name, value))}
      {attributes.map(([key, value]) => row({ attribute: key }, <span className="font-mono">{key}</span>, value))}
    </tbody></table>
    {record.resource && Object.keys(record.resource).length > 0 && <div><div className="mb-1.5 flex items-center gap-2 text-xs text-muted-foreground">{text('resource')}<Count>JSON</Count></div>
      <JSONViewer label={text('resource')} value={JSON.stringify(record.resource, null, 2)} mode={mode} height={160} /></div>}
  </div>;
}

/** A condition chip that edits in a popover; without a chip, "+ Filter" adds one. */
function ChipEditor({ chip, attributes, facets, onChange, onRemove }: {
  chip?: AuditChip; attributes: readonly string[]; facets?: readonly AuditFacet[]; onChange: (chip: AuditChip) => void; onRemove?: () => void;
}) {
  const text = usePlatformText(), [open, setOpen] = useState(false);
  const [target, setTarget] = useState<AuditTarget>(chip?.target ?? { field: 'service' }), [op, setOp] = useState<AuditOp>(chip?.op ?? 'is');
  const [values, setValues] = useState<JsonValue[]>(chip?.values ?? []), [draft, setDraft] = useState('');
  useEffect(() => { if (open) { setTarget(chip?.target ?? { field: 'service' }); setOp(chip?.op ?? 'is'); setValues(chip?.values ?? []); setDraft(''); } }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
  const ops = 'field' in target ? fieldOps : auditOps;
  const suggestions = facets?.find((facet) => sameTarget(targetOf(facet), target))?.values.map((entry) => jsonOf(entry.value)) ?? [];
  const targets = [...auditFields.map((field) => ({ value: `field:${field}`, label: text(`auditField_${field}`) })), ...attributes.map((key) => ({ value: `attr:${key}`, label: key }))];
  const pending = draft.trim() ? [...values, numeric(op) ? Number(draft) : parseValue(draft)] : values;
  const ready = !takesValues(op) || (numeric(op) ? pending.length === 1 && typeof pending[0] === 'number' && !Number.isNaN(pending[0]) : pending.length > 0);
  const apply = () => { onChange({ target, op, values: takesValues(op) ? pending : [] }); setOpen(false); };
  const title = chip && <><span className="font-mono">{'field' in chip.target ? text(`auditField_${chip.target.field}`) : chip.target.attribute}</span>
    <span className="text-muted-foreground">{text(`auditOp_${chip.op}`)}</span>{takesValues(chip.op) && <span className="max-w-48 truncate font-mono">{chip.values.map(label).join(', ')}</span>}</>;
  return <Popover open={open} onOpenChange={setOpen}>
    {chip ? <Badge variant="outline" className={`h-7 gap-1.5 pr-0.5 text-xs ${chip.op === 'is_not' || chip.op === 'not_contains' || chip.op === 'not_exists' ? 'border-destructive/40' : 'border-primary/40'}`}>
      <PopoverTrigger asChild><button type="button" className="inline-flex items-center gap-1.5">{title}</button></PopoverTrigger>
      <button type="button" aria-label={text('removeFilter', { name: targetKey(chip.target) })} className="grid size-5 place-items-center rounded-sm hover:bg-raised" onClick={onRemove}><X className="size-3" /></button></Badge>
      : <PopoverTrigger asChild><Button size="sm" variant="outline"><Plus />{text('newFilter')}</Button></PopoverTrigger>}
    <PopoverContent align="start" className="grid w-80 gap-3">
      <Combobox label={text('filterField')} options={targets} values={[targetKey(target)]} onValuesChange={(next) => {
        const picked = next[0]; if (!picked) return;
        const nextTarget: AuditTarget = picked.startsWith('field:') ? { field: picked.slice(6) as AuditFieldName } : { attribute: picked.slice(5) };
        setTarget(nextTarget); setValues([]); if ('field' in nextTarget && !fieldOps.includes(op)) setOp('is');
      }} />
      <NativeSelect aria-label={text('filterOperator')} value={op} onChange={(event) => setOp(event.target.value as AuditOp)}>
        {ops.map((entry) => <option key={entry} value={entry}>{text(`auditOp_${entry}`)}</option>)}</NativeSelect>
      {takesValues(op) && <div className="grid gap-2">
        {values.length > 0 && <div className="flex flex-wrap gap-1">{values.map((value, at) => <Badge key={at} variant="outline" className="gap-1 font-mono">{label(value)}
          <button type="button" aria-label={text('removeFilter', { name: label(value) })} onClick={() => setValues(values.filter((_, other) => other !== at))}><X className="size-3" /></button></Badge>)}</div>}
        <Input aria-label={text('filterValue')} placeholder={text(numeric(op) ? 'filterNumber' : 'filterValueHint')} inputMode={numeric(op) ? 'decimal' : undefined} value={draft}
          onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); if (numeric(op) || !draft.trim()) { if (ready) apply(); } else { setValues(pending); setDraft(''); } } }} />
        {!numeric(op) && suggestions.length > 0 && <div className="flex max-h-32 flex-wrap gap-1 overflow-auto">{suggestions.filter((value) => !values.some((v) => JSON.stringify(v) === JSON.stringify(value))).map((value) =>
          <button key={JSON.stringify(value)} type="button" className="h-6 rounded-sm border border-border px-2 font-mono text-2xs hover:border-border-strong" onClick={() => setValues([...values, value])}>{label(value)}</button>)}</div>}
      </div>}
      <div className="flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={() => setOpen(false)}>{text('cancel')}</Button><Button size="sm" disabled={!ready} onClick={apply}>{text('applyFilter')}</Button></div>
    </PopoverContent>
  </Popover>;
}
