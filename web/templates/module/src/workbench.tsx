import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { SchemaSchema, structToNative } from '@gopherex/schemapb';
import { usePluginContext } from '@gopherex/backplane-plugin-sdk';
import { SchemaForm } from '@gopherex/backplane-schema-forms';
import { QueryEditor } from '@gopherex/backplane-editors';
import { TimeSeriesChart } from '@gopherex/backplane-charts';
import { LogViewer } from '@gopherex/backplane-observability-ui';
import { ConnectionNotice, nativeJSON } from '@gopherex/backplane-platform-ui';

const schema = create(SchemaSchema, { id: { name: 'input' }, fields: [{ name: 'sequence', title: 'Sequence', kind: { case: 'uint64', value: { default: 18446744073709551615n } } }] });
const series = [{ id: 'latency', label: 'hello', points: [{ x: 1790683200000, value: '12.5' }, { x: 1790683201000, value: '18.25' }] }];
const records = [{ id: 'local-1', timestamp: '2026-09-29T12:00:00.123456789Z', body: 'Local fixture request completed', severity: 'INFO', traceId: '0123456789abcdef0123456789abcdef' }];
export default function Workbench() {
  const { mode } = usePluginContext(), { t } = useTranslation('module.hello');
  const [query, setQuery] = useState('{ resource.service.name = "hello" }'), [saved, setSaved] = useState('');
  return <section style={{ display: 'grid', gap: 24 }}><h1>{t('workbench')}</h1><ConnectionNotice />
    <QueryEditor label={t('query')} language="traceql" languages={['traceql']} onLanguageChange={() => {}} value={query} onChange={setQuery} mode={mode} />
    <SchemaForm label={t('configuration')} schema={schema} onSubmit={async (baked) => { setSaved(nativeJSON(structToNative(baked.values!))); }} />
    {saved && <output aria-label={t('saved', { name: '' })}>{saved}</output>}
    <TimeSeriesChart label={t('samples')} series={series} mode={mode} height={220} />
    <LogViewer label={t('logs')} records={records} mode={mode} />
  </section>;
}
