import { lazy, Suspense, useState } from 'react';
import { Link, NavLink, Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Boxes, ChevronRight, Moon, Sun, PanelLeft, LogOut, ArrowUpRight } from 'lucide-react';
import { Button, ErrorBoundary, Input } from '@gopherex/backplane-ui';
import { useConnection } from '@gopherex/backplane-react';
import { PluginProvider } from '@gopherex/backplane-plugin-sdk';
import { AuditFeed, ExplorePanel, ServiceInspector, ConfigurationPanel, ServiceOperations, EventStreams, WorkflowRuns, SchedulesPanel } from '@gopherex/backplane-platform-ui';
import type { ServiceSummary } from '@gopherex/backplane-api';
import type { ThemeMode } from '@gopherex/backplane-theme';
import Sidebar from './Sidebar';
import type { useRegistry, ModuleEntry } from './registry';
import './console.css';

const Development = lazy(() => import('../dev'));
const serviceTabs = ['overview', 'configuration', 'operations', 'events', 'runs', 'schedules'] as const;

function Services({ services }: { services: ServiceSummary[] }) {
  const { t } = useTranslation('console'), [filter, setFilter] = useState('');
  return <>
    <div className="console-page-heading"><div><h1>{t('services')}</h1><p>{t('servicesDescription')}</p></div></div>
    <div className="console-summary">{[
      [t('registered'), services.length], [t('instances'), services.reduce((sum, service) => sum + service.instances, 0)], [t('healthy'), services.reduce((sum, service) => sum + service.healthy, 0)],
    ].map(([label, value]) => <div key={label}><span>{label}</span><strong>{value}</strong></div>)}</div>
    <div className="console-list-toolbar"><h2>{t('services')} <span className="console-count">{services.length}</span></h2><Input aria-label={t('filter')} placeholder={t('filter')} value={filter} onChange={(event) => setFilter(event.target.value)} /></div>
    <div className="console-table-scroll"><table className="console-service-table"><thead><tr><th>{t('service')}</th><th>{t('health')}</th><th>{t('version')}</th><th>{t('instances')}</th><th>{t('pages')}</th></tr></thead><tbody>
      {services.filter((service) => service.name.toLowerCase().includes(filter.toLowerCase())).map((service) => <tr key={service.name}>
        <td><Link to={`/services/${service.name}`}><Boxes size={17} /><strong>{service.name}</strong><ArrowUpRight size={14} /></Link></td>
        <td><span className="console-health" data-health={service.health}><i />{t(service.health === 1 ? 'ready' : service.health === 2 ? 'degraded' : 'down')}</span></td>
        <td><code>{service.latestVersion || '—'}</code></td><td>{service.healthy} / {service.instances}</td><td>{service.ui ? <ChevronRight size={16} aria-label={t('servicePages')} /> : '—'}</td>
      </tr>)}
    </tbody></table></div>
    {!services.some((service) => service.name.toLowerCase().includes(filter.toLowerCase())) && <p className="console-empty">{t(services.length ? 'empty' : 'noServices')}</p>}
  </>;
}

function ServicePage({ mode }: { mode: ThemeMode }) {
  const { t } = useTranslation('console'), { service = '', tab = 'overview' } = useParams();
  return <><div className="console-page-heading"><div><Link className="console-back" to="/services">{t('services')}</Link><h1>{service}</h1></div></div>
    <nav className="console-tabs" aria-label={t('admin', { service })}>{serviceTabs.map((name) => <NavLink end key={name} to={`/services/${service}${name === 'overview' ? '' : `/${name}`}`}>{t(name)}</NavLink>)}</nav>
    <div className="console-detail" key={`${service}/${tab}`}>
      {tab === 'overview' && <ServiceInspector service={service} mode={mode} />}
      {tab === 'configuration' && <ConfigurationPanel service={service} mode={mode} />}
      {tab === 'operations' && <ServiceOperations service={service} mode={mode} />}
      {tab === 'events' && <EventStreams service={service} mode={mode} />}
      {tab === 'runs' && <WorkflowRuns service={service} mode={mode} />}
      {tab === 'schedules' && <SchedulesPanel service={service} mode={mode} />}
      {!serviceTabs.includes(tab as typeof serviceTabs[number]) && <NotFound />}
    </div>
  </>;
}
function NotFound() { const { t } = useTranslation('console'); return <><h1>{t('notFound')}</h1><Link to="/services">{t('back')}</Link></>; }
function ModulePage({ modules, mode, retry, loading }: { modules: Record<string, ModuleEntry>; mode: ThemeMode; retry: () => void; loading: boolean }) {
  const { service = '' } = useParams(), { t } = useTranslation('console'), entry = modules[service];
  if (!entry) return <p>{t(loading ? 'loading' : 'unavailable')}</p>;
  if (entry.error) return <div role="alert"><h1>{t('failed')}</h1><p>{entry.error}</p><Button onClick={retry}>{t('retry')}</Button></div>;
  const plugin = entry.plugin;
  if (!plugin) return <p role="status">{t('loading')}</p>;
  return <PluginProvider context={{ service, basePath: `/s/${service}`, mode, environment: 'embedded' }}><ErrorBoundary resetKey={plugin} fallback={(error, reset) => <div role="alert"><h1>{t('failed')}</h1><p>{error.message}</p><Button onClick={reset}>{t('retry')}</Button></div>}><plugin.Routes /></ErrorBoundary></PluginProvider>;
}

export default function Console({ mode, onThemeChange, onLogout, registry }: { mode: ThemeMode; onThemeChange: (mode: ThemeMode) => void; onLogout: () => Promise<void>; registry: ReturnType<typeof useRegistry> }) {
  const { t } = useTranslation('console'), location = useLocation(), connection = useConnection();
  const services = registry.catalog.value?.services ?? [];
  const [mobile, setMobile] = useState(false);
  const [logoutPending, setLogoutPending] = useState(false), [logoutFailed, setLogoutFailed] = useState(false);
  const parts = location.pathname.split('/').filter(Boolean), section = parts[0] === 's' ? parts[1] : t(parts[0] || 'services');
  return <div className="console-shell">
    <a className="console-skip" href="#console-content">{t('skip')}</a>
    <header className="console-header">
      <Button id="console-nav-toggle" variant="ghost" size="icon" aria-label={t(mobile ? 'close' : 'open')} aria-controls="console-navigation" aria-expanded={mobile} onClick={() => { if (matchMedia('(max-width: 760px)').matches) setMobile((value) => !value); else document.querySelector<HTMLElement>('#console-navigation a')?.focus(); }}><PanelLeft size={18} /></Button>
      <Link to="/services" className="console-brand"><span className="console-brand-mark"><Boxes size={19} /></span>{t('brand')}</Link>
      <span className="console-header-divider" /><span className="console-breadcrumb">{section}</span>
      <div className="console-header-actions"><span className="console-connection" data-connected={connection.connection === 'connected'}><i />{t(connection.connection === 'connected' ? 'connected' : 'disconnected', { state: connection.connection })}</span>
        <Button variant="ghost" size="icon" aria-label={t(mode === 'dark' ? 'light' : 'dark')} title={t(mode === 'dark' ? 'light' : 'dark')} onClick={() => onThemeChange(mode === 'dark' ? 'light' : 'dark')}>{mode === 'dark' ? <Sun size={17} /> : <Moon size={17} />}</Button>
        <Button variant="ghost" size="icon" disabled={logoutPending} aria-label={t('logout')} title={t('logout')} onClick={() => { setLogoutPending(true); setLogoutFailed(false); void onLogout().catch(() => setLogoutFailed(true)).finally(() => setLogoutPending(false)); }}><LogOut size={17} /></Button>
      </div>
    </header>
    <div className="console-body"><Sidebar services={services} modules={registry.modules} mobile={mobile} onClose={() => setMobile(false)} />
      <main className="console-content" id="console-content" tabIndex={-1}>
        {logoutFailed && <p role="alert">{t('logoutFailed')}</p>}
        {registry.catalog.status === 'loading' && <p role="status">{t('catalogLoading')}</p>}
        {registry.catalog.status === 'stale' && <p role="status">{t('stale')}</p>}
        {registry.catalog.status === 'error' && <p role="alert">{t('catalogError')}</p>}
        {registry.descriptors.error !== undefined && <div role="alert">{t('registryError')} <Button onClick={registry.retry}>{t('retry')}</Button></div>}
        <Routes>
          <Route path="/" element={<Navigate to="/services" replace />} />
          <Route path="/services" element={<Services services={services} />} />
          <Route path="/services/:service/:tab?" element={<ServicePage mode={mode} />} />
          <Route path="/explore" element={<><div className="console-page-heading"><div><h1>{t('explore')}</h1><p>{t('exploreDescription')}</p></div></div><ExplorePanel mode={mode} /></>} />
          <Route path="/audit" element={<><div className="console-page-heading"><div><h1>{t('audit')}</h1><p>{t('auditDescription')}</p></div></div><AuditFeed mode={mode} /></>} />
          <Route path="/s/:service/*" element={<ModulePage modules={registry.modules} mode={mode} retry={registry.retry} loading={registry.descriptors.loading} />} />
          <Route path="/dev" element={<Suspense fallback={<p>{t('loading')}</p>}><Development mode={mode} /></Suspense>} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </main>
    </div>
  </div>;
}
