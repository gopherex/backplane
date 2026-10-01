import { PlatformExecutionProvider } from '@gopherex/backplane-platform-ui';
import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { PlatformServiceClient, GetCapabilitiesRequestSchema, GetInfrastructureRequestSchema } from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { usePlatformQuery } from '@gopherex/backplane-platform-ui';
import { Button, EmptyState, PageHeader, Panel, StatusBadge, Timestamp } from '@gopherex/backplane-ui';
import { useTranslation } from 'react-i18next';

function usePlatformState() {
  const client = useClient(PlatformServiceClient);
  const capabilities = usePlatformQuery('platform:capabilities', (signal) => client.getCapabilities(create(GetCapabilitiesRequestSchema), { signal }));
  const health = usePlatformQuery('platform:health', (signal) => client.getInfrastructure(create(GetInfrastructureRequestSchema), { signal }));
  useEffect(() => { const timer = setInterval(health.refresh, 5000); return () => clearInterval(timer); }, [health.refresh]);
  useEffect(() => { const timer = setInterval(capabilities.refresh, 30000); return () => clearInterval(timer); }, [capabilities.refresh]);
  return { capabilities, health };
}
const Context = createContext<ReturnType<typeof usePlatformState> | null>(null);
export function PlatformProvider({ children }: { children: ReactNode }) {
  const state = usePlatformState();
  const enabled = (name: string) => state.capabilities.value?.capabilities.some((entry) => entry.name === name && entry.enabled) ?? false;
  return <Context.Provider value={state}><PlatformExecutionProvider value={{ bindings: enabled('bindings'), rules: enabled('rules') }}>{children}</PlatformExecutionProvider></Context.Provider>;
}
export function useCapabilities() { return useContext(Context)?.capabilities.value?.capabilities; }
const dependencies: Record<string, string[]> = { events: ['nats'], workflows: ['temporal'], schedules: ['temporal'], bindings: ['temporal'], rules: ['nats', 'temporal'], metrics: ['metrics'], logs: ['logs'], traces: ['traces'] };
export function FeatureGate({ feature, children }: { feature: string; children: ReactNode }) {
  const state = useContext(Context), { t } = useTranslation('console');
  if (!state) return children;
  if (!state.capabilities.value) return <EmptyState title={t(state.capabilities.error ? 'capabilitiesError' : 'loading')} action={state.capabilities.error ? <Button onClick={state.capabilities.refresh}>{t('retry')}</Button> : undefined} />;
  if (!state.capabilities.value.capabilities.some((capability) => capability.name === feature && capability.enabled)) return <EmptyState title={t('featureDisabled')} description={t('featureDisabledHint')} />;
  const unavailable = state.health.value?.dependencies.some((entry) => dependencies[feature]?.includes(entry.name) && entry.checkedAt && !entry.ok);
  return <>{unavailable && <p role="status" className="console-banner" data-tone="warning">{t('featureUnavailable')}</p>}{children}</>;
}
export function InfrastructurePage() {
  const state = useContext(Context)!, { t } = useTranslation('console');
  return <><PageHeader title={t('infrastructure')} description={t('infrastructureDescription')} actions={<Button variant="outline" onClick={state.health.refresh} disabled={state.health.loading}>{t('refresh')}</Button>} />
    <div className="console-scroll grid content-start gap-4">
      {state.health.error !== undefined && <p role="alert">{t('infrastructureError')}</p>}
      {!state.health.value && <p role="status">{t('loading')}</p>}
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{state.health.value?.dependencies.map((entry) => <Panel key={entry.name} title={t(`infraNames.${entry.name}`, { defaultValue: entry.name })}>
        <div className="flex items-center justify-between gap-3"><StatusBadge tone={!entry.checkedAt ? 'neutral' : entry.ok ? 'success' : 'danger'}>{entry.checkedAt ? entry.ok ? 'OK' : 'NO' : t('checking')}</StatusBadge><span className="text-xs text-muted-foreground">{t(entry.required ? 'requiredDependency' : 'optionalDependency')}</span></div>
        <p className="mt-2 text-xs text-muted-foreground">{entry.checkedAt ? <Timestamp value={timestampDate(entry.checkedAt)} absolute /> : t('checking')}{entry.reason && entry.checkedAt ? ` · ${t(`infraReason.${entry.reason}`)}` : ''}</p>
      </Panel>)}</div>
      {state.health.value && <p className="text-xs text-muted-foreground">{t('checkedBy', { instance: state.health.value.instanceId })}</p>}
      <Panel title={t('deploymentFeatures')}><div className="flex flex-wrap gap-2">{state.capabilities.value?.capabilities.map((entry) => <StatusBadge key={entry.name} tone={entry.enabled ? 'success' : 'neutral'}>{t(`featureNames.${entry.name}`, { defaultValue: entry.name })}: {t(entry.enabled ? 'enabledFeature' : 'disabledFeature')}</StatusBadge>)}</div></Panel>
    </div>
  </>;
}
