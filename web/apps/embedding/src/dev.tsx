import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AuditFeed, BindingEditor, ConfigurationPanel, EventStreams, ExplorePanel, ServiceCatalog, ServiceInspector, ServiceOperations, WorkflowRuns, SchedulesPanel } from '@gopherex/backplane-platform-ui';
import { Button } from '@gopherex/backplane-ui';
export default function Development({ mode }: { mode: 'dark' | 'light' }) {
  const { t } = useTranslation('host'), [service, setService] = useState('hello'), [view, setView] = useState('services');
  return <section><h1>{t('development')}</h1><p>{t('developmentDescription')}</p>
    <nav aria-label={t('development')}>{['services', 'configuration', 'operations', 'binding', 'events', 'runs', 'schedules', 'audit', 'explore'].map((name) => <Button key={name} variant={view === name ? 'default' : 'outline'} aria-pressed={view === name} onClick={() => setView(name)}>{t(name)}</Button>)}</nav>
    {view === 'services' && <><ServiceCatalog onSelect={(service) => setService(service.name)} /><ServiceInspector service={service} mode={mode} /></>}
    {view === 'configuration' && <ConfigurationPanel service={service} mode={mode} />}
    {view === 'operations' && <ServiceOperations service={service} mode={mode} />}
    {view === 'binding' && <BindingEditor hook="hello.Greet" mode={mode} />}
    {view === 'events' && <EventStreams service={service} mode={mode} />}
    {view === 'runs' && <WorkflowRuns service={service} mode={mode} />}
    {view === 'schedules' && <SchedulesPanel service={service} mode={mode} />}
    {view === 'audit' && <AuditFeed mode={mode} />}
    {view === 'explore' && <ExplorePanel mode={mode} />}
  </section>;
}
