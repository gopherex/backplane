import { useCallback, useMemo, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { ObsServiceClient, ObsSignal, ObsLanguage, GetObsCapabilitiesRequestSchema, ListObsSourcesRequestSchema, ListObsFieldsRequestSchema, GetTraceRequestSchema, QueryObsRequestSchema, QueryObsResponseSchema, type QueryObsResponse } from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { Button, Checkbox, DataTable, Input, NativeSelect } from '@gopherex/backplane-ui';
import { JSONViewer, QueryEditor, type QueryLanguage } from '@gopherex/backplane-editors';
import { TimeSeriesChart } from '@gopherex/backplane-charts';
import { LogViewer, TraceList, TraceWaterfall, type CorrelationTarget } from '@gopherex/backplane-observability-ui';
import { rowsToLogs, seriesToCharts, tempoToSpans, traceSearchToList } from './telemetry-model.js';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
const languageNames: Partial<Record<ObsLanguage, QueryLanguage>> = { [ObsLanguage.LOGSQL]: 'logsql', [ObsLanguage.METRICSQL]: 'metricsql', [ObsLanguage.PROMQL]: 'promql', [ObsLanguage.TRACEQL]: 'traceql' };
const languageIds: Record<string, ObsLanguage> = { logsql: ObsLanguage.LOGSQL, metricsql: ObsLanguage.METRICSQL, promql: ObsLanguage.PROMQL, traceql: ObsLanguage.TRACEQL };

export function ExplorePanel({ mode, onNavigate }: { mode: 'dark' | 'light'; onNavigate?: (target: CorrelationTarget) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const [signal, setSignal] = useState(ObsSignal.LOGS), [language, setLanguage] = useState<QueryLanguage>('logsql'), [expression, setExpression] = useState('*');
  const [start, setStart] = useState(String(BigInt(Date.now() - 15 * 60000) * 1000000n)), [end, setEnd] = useState(String(BigInt(Date.now()) * 1000000n));
  const [limit, setLimit] = useState('100'), [step, setStep] = useState('0'), [statistics, setStatistics] = useState(false), [sourceFilter, setSourceFilter] = useState('');
  const [traceId, setTraceId] = useState(''), [lookup, setLookup] = useState('');
  const capabilities = usePlatformQuery('obs:capabilities', (signal) => client.getObsCapabilities(create(GetObsCapabilitiesRequestSchema), { signal }));
  const capability = capabilities.value?.signals.find((item) => item.signal === signal);
  const languages = capability?.languages.flatMap((item) => languageNames[item] ? [languageNames[item]!] : []) ?? [];
  const selectedLanguage = languages.includes(language) ? language : languages[0];
  const action = usePlatformAction<QueryObsResponse>();
  const range = () => ({ startUnixNano: BigInt(start), endUnixNano: BigInt(end) });
  const sourceKey = JSON.stringify([signal, start, end]);
  const sources = usePlatformQuery(`obs:sources:${sourceKey}`, (abort) => client.listObsSources(create(ListObsSourcesRequestSchema, { signal, range: range(), limit: 100 }), { signal: abort }));
  const complete = useCallback(async ({ signal: abort }: { signal: AbortSignal }) => {
    const fields = await client.listObsFields(create(ListObsFieldsRequestSchema, { signal, range: { startUnixNano: BigInt(start), endUnixNano: BigInt(end) }, filter: sourceFilter, limit: 100 }), { signal: abort });
    return fields.fields.map((label) => ({ label, type: 'property' as const }));
  }, [client, signal, start, end, sourceFilter]);
  const run = () => void action.run((abort) => client.queryObs(create(QueryObsRequestSchema, { signal, language: languageIds[selectedLanguage], expression, range: range(), limit: Number(limit), stepNanos: BigInt(step), statistics }), { signal: abort }));
  const result = action.value;
  const navigate = (target: CorrelationTarget) => { if (target.signal === 'traces' && target.traceId) { setTraceId(target.traceId); setLookup(target.traceId); } else onNavigate?.(target); };
  return <section aria-label="Explore" style={{ display: 'grid', gap: 12 }}><QueryState state={capabilities} />
    <NativeSelect aria-label={text('signal')} value={signal} onChange={(event) => { setSignal(Number(event.target.value)); setStatistics(false); }}>
      {[ObsSignal.LOGS, ObsSignal.METRICS, ObsSignal.TRACES].map((value) => <option key={value} value={value}>{text(value === ObsSignal.LOGS ? 'logs' : value === ObsSignal.METRICS ? 'metrics' : 'traces')}</option>)}
    </NativeSelect><div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
      <Input aria-label={text('start')} value={start} onChange={(event) => setStart(event.target.value)} /><Input aria-label={text('end')} value={end} onChange={(event) => setEnd(event.target.value)} />
      <Input aria-label={text('limit')} value={limit} onChange={(event) => setLimit(event.target.value)} /><Input aria-label={text('step')} value={step} onChange={(event) => setStep(event.target.value)} />
    </div><Input aria-label={text('filter')} value={sourceFilter} onChange={(event) => setSourceFilter(event.target.value)} />
    {signal === ObsSignal.LOGS && <label style={{ display: 'flex', gap: 8 }}><Checkbox checked={statistics} onCheckedChange={(value) => setStatistics(value === true)} />{text('statistics')}</label>}
    {selectedLanguage ? <QueryEditor label={text('expression')} mode={mode} value={expression} onChange={setExpression} language={selectedLanguage} languages={languages} onLanguageChange={setLanguage} complete={complete} onSubmit={run} disabled={action.pending || action.disabled} /> : <p role="status">{text('noCapability')}</p>}
    <MutationState action={action} /><QueryState state={sources}><DataTable label={text('sources')} data={sources.value?.sources.map((source) => ({ id: JSON.stringify(source.resource), ...source.resource })) ?? []} getRowId={(source) => source.id} partial={sources.value?.info?.truncated || sources.value?.info?.partial} columns={[
      { id: 'resource', label: text('source'), value: (source) => source.id, width: 700 },
    ]} height={200} /></QueryState>
    {result?.info && <>{(result.info.partial || result.info.truncated) && <p role="status">{text('partial')}</p>}{!!result.info.warnings.length && <ul aria-label={text('warnings')}>{result.info.warnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul>}</>}
    {result && <ObsResults result={result} mode={mode} onNavigate={navigate} />}
    {capabilities.value?.signals.some((item) => item.traceLookup) && <form style={{ display: 'flex', gap: 8 }} onSubmit={(event) => { event.preventDefault(); setLookup(traceId); }}><Input aria-label={text('traceId')} value={traceId} onChange={(event) => setTraceId(event.target.value)} /><Button type="submit">{text('lookup')}</Button></form>}
    {lookup && <TraceLookup traceId={lookup} mode={mode} onNavigate={navigate} />}
  </section>;
}
export function ObsResults({ result, mode, onNavigate }: { result: QueryObsResponse; mode: 'dark' | 'light'; onNavigate?: (target: CorrelationTarget) => void }) {
  const text = usePlatformText(), id = useMemo(() => crypto.randomUUID(), [result]);
  return <>{result.result.case === 'rows' && <LogViewer label={text('logs')} records={rowsToLogs(result.result.value, id)} mode={mode} partial={result.info?.partial || result.info?.truncated} onNavigate={onNavigate} />}
    {result.result.case === 'timeSeries' && <TimeSeriesChart label={text('metrics')} series={seriesToCharts(result.result.value)} mode={mode} partial={result.info?.partial || result.info?.truncated} />}
    {result.result.case === 'traces' && <TraceList label={text('traces')} traces={traceSearchToList(result.result.value)} onSelect={(trace) => onNavigate?.({ signal: 'traces', traceId: trace.id })} partial={result.info?.partial || result.info?.truncated} />}
    <details><summary>{text('raw')}</summary><JSONViewer label={text('raw')} value={toJsonString(QueryObsResponseSchema, result, { prettySpaces: 2 })} mode={mode} /></details>
  </>;
}
export function TraceLookup({ traceId, mode, onNavigate }: { traceId: string; mode: 'dark' | 'light'; onNavigate?: (target: CorrelationTarget) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`obs:trace:${traceId}`, async (signal) => { const response = await client.getTrace(create(GetTraceRequestSchema, { traceId }), { signal }); const raw = new TextDecoder().decode(response.tempoJson); return { response, raw, spans: tempoToSpans(raw) }; });
  return <QueryState state={state}>{state.value && <><TraceWaterfall label={traceId} spans={state.value.spans} partial={state.value.response.info?.partial || state.value.response.info?.truncated} onNavigate={onNavigate} />
    <details><summary>{text('raw')}</summary><JSONViewer label={text('raw')} value={state.value.raw} mode={mode} /></details></>}</QueryState>;
}
