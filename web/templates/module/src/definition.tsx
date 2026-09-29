import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, ListServicesRequestSchema, type ListServicesResponse } from '@gopherex/backplane-api';
import { AdminServiceClient } from './gen/proto/hello/console/v1/admin_ws_pb';
import { GetStatsRequestSchema } from './gen/proto/hello/console/v1/admin_pb';
import { usePlatformQuery, QueryState } from '@gopherex/backplane-platform-ui';
import { useClient } from '@gopherex/backplane-react';
import { definePlugin, usePluginContext, usePluginNavigate } from '@gopherex/backplane-plugin-sdk';
import { Button, Input, Label, Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@gopherex/backplane-ui';
import navigation from './navigation';
import { lazy, Suspense } from 'react';
const Workbench = lazy(() => import('./workbench'));
function KitWorkbench() { const { t } = useTranslation('module.hello'); return <Suspense fallback={<p role="status">{t('loading')}</p>}><Workbench /></Suspense>; }

function Overview() {
  const { t } = useTranslation('module.hello');
  const context = usePluginContext();
  const navigate = usePluginNavigate();
  const api = useClient(CatalogServiceClient);
  const [services, setServices] = useState<ListServicesResponse>();
  const [error, setError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void api.listServices(create(ListServicesRequestSchema), { signal: controller.signal }).then(
      (response) => { if (!controller.signal.aborted) setServices(response); },
      () => { if (!controller.signal.aborted) setError(true); },
    );
    return () => controller.abort();
  }, [api]);
  return <section><h1>{t('title')}</h1><p>{t(context.environment)}</p><h2>{t('services')}</h2>
    {error ? <p role="alert">{t('error')}</p> : !services ? <p role="status">{t('loading')}</p> :
      <Table><TableHeader><TableRow><TableHead>{t('service')}</TableHead><TableHead>{t('instanceCount')}</TableHead></TableRow></TableHeader>
        <TableBody>{services.services.map((service) => <TableRow key={service.name}><TableCell>{service.name}</TableCell><TableCell>{service.instances}</TableCell></TableRow>)}</TableBody>
      </Table>}
    <ServiceStats /><Button onClick={() => navigate('settings')}>{t('toSettings')}</Button>
  </section>;
}
function ServiceStats() {
  const { t } = useTranslation('module.hello'), client = useClient(AdminServiceClient);
  const state = usePlatformQuery('hello:stats', (signal) => client.getStats(create(GetStatsRequestSchema), { signal }));
  return <section><h2>{t('stats')}</h2><Button variant="outline" onClick={state.refresh}>{t('refresh')}</Button><QueryState state={state}>{state.value && <dl>
    <dt>{t('greetings')}</dt><dd data-testid="greetings">{String(state.value.greetings)}</dd>
    <dt>{t('audited')}</dt><dd data-testid="audited">{String(state.value.audited)}</dd>
    <dt>{t('greeting')}</dt><dd>{state.value.currentGreeting}</dd>
  </dl>}</QueryState></section>;
}
function Settings() {
  const { t } = useTranslation('module.hello');
  const navigate = usePluginNavigate();
  const [name, setName] = useState('Hello');
  const [saved, setSaved] = useState('');
  return <section><h1>{t('settings')}</h1>
    <form onSubmit={(event) => { event.preventDefault(); setSaved(name); }}>
      <Label htmlFor="name">{t('displayName')}</Label><Input id="name" value={name} onChange={(event) => setName(event.target.value)} required />
      <Button type="submit">{t('save')}</Button>
    </form><p role="status">{saved ? t('saved', { name: saved }) : ''}</p>
    <Button variant="outline" onClick={() => navigate('')}>{t('toOverview')}</Button>
  </section>;
}
function Missing() { const { t } = useTranslation('module.hello'); return <h1>{t('missing')}</h1>; }
export const definition = definePlugin({ ...navigation, routes: [
  { index: true, element: <Overview /> }, { path: 'settings', element: <Settings /> }, { path: 'workbench', element: <KitWorkbench /> }, { path: '*', element: <Missing /> },
] });
