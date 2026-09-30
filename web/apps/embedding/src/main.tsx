import 'virtual:backplane-grafana-assets';
import { lazy, Suspense, useEffect, useLayoutEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Routes, Route, Link } from 'react-router-dom';
import i18next from 'i18next';
import { initReactI18next, I18nextProvider, useTranslation } from 'react-i18next';
import { ThemeContext } from '@grafana/ui';
import { createGrafanaTheme, type ThemeMode } from '@gopherex/backplane-theme';
import { BackplaneProvider, useConnection } from '@gopherex/backplane-react';
import { PluginProvider, installPluginTranslations } from '@gopherex/backplane-plugin-sdk';
import { loadPlugin, type LoadedPlugin } from '@gopherex/backplane-plugin-sdk/host';
import { Button, ErrorBoundary, englishResources } from '@gopherex/backplane-ui';
import { BackplaneClient } from '@gopherex/backplane-client';
import { CatalogServiceClient, ListPluginsRequestSchema } from '@gopherex/backplane-api';
import { create } from '@bufbuild/protobuf';
import { fixtureClient } from '../../../templates/module/src/fixtures';
import '@gopherex/backplane-ui/style.css';
import '@gopherex/backplane-theme/style.css';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import { consoleEnglish } from './console/locales';
import { Login } from './console/Login';
import { loginEnglish } from './console/login-locales';
import { consoleErrors } from './console/errors';

const Console = lazy(() => import('./console/ConsoleRuntime'));

const Development = lazy(() => import('./dev'));
const live = document.querySelector('meta[name=backplane-fixture]')?.getAttribute('content') === 'live';
// The compatibility fixture renders the module template's standalone page and
// its global element styles; the live console must not inherit them.
if (!live) await import('../../../templates/module/src/standalone.css');
const consoleBase = import.meta.env.BASE_URL;
// The live console reports its own errors through the installation's ingest.
const errors = live ? consoleErrors(consoleBase) : undefined;
const runtime = live ? new BackplaneClient({ baseURL: new URL(consoleBase, location.origin).href }) : fixtureClient;
const i18n = i18next.createInstance();
await i18n.use(initReactI18next).init({ lng: 'en', fallbackLng: 'en', supportedLngs: ['en'], interpolation: { escapeValue: false }, resources: { en: {
  'backplane.ui': englishResources,
  console: consoleEnglish,
  login: loginEnglish,
  host: { development: 'Development acceptance', developmentDescription: 'Live kit components. Product console design is a separate review.', services: 'Services', configuration: 'Configuration', operations: 'Operations', wiring: 'Wiring', automation: 'Automation', events: 'Events', runs: 'Runs', schedules: 'Schedules', audit: 'Audit', explore: 'Explore', title: 'Embedding fixture', home: 'Platform home', loading: 'Loading module', error: 'Module could not be loaded', light: 'Light theme', dark: 'Dark theme', token: 'Operator token', login: 'Log in', logout: 'Log out', loginError: 'Login failed', state: 'Connection: {{state}}' },
} } });

function Host() {
  const { t } = useTranslation('host');
  const connection = useConnection();
  const [mode, setMode] = useState<ThemeMode>(() => { try { return live && localStorage.getItem('backplane.theme') === 'light' ? 'light' : 'dark'; } catch { return 'dark'; } });
  const [plugin, setPlugin] = useState<LoadedPlugin>();
  const [error, setError] = useState<string>();
  useLayoutEffect(() => { document.documentElement.dataset.theme = mode; document.documentElement.classList.toggle('dark', mode === 'dark'); }, [mode]);
  useEffect(() => { if (live) try { localStorage.setItem('backplane.theme', mode); } catch { /* Optional preference. */ } }, [mode]);
  useEffect(() => {
    if (live || connection.connection !== 'connected') return;
    const controller = new AbortController();
    setError(undefined);
    const descriptor = runtime instanceof BackplaneClient ? runtime.client(CatalogServiceClient).listPlugins(create(ListPluginsRequestSchema), { signal: controller.signal }).then((result) => {
      const plugin = result.plugins.find((entry) => entry.service === 'hello' && entry.available);
      if (!plugin) throw new Error('Hello plugin unavailable');
      return plugin;
    }) : Promise.resolve({ service: 'hello', hash: 'fixture-v1', sdkMajor: 0, path: '/backplane/plugins/hello/fixture-v1/' });
    void descriptor.then((plugin) => loadPlugin(plugin, { consoleURL: `${location.origin}/backplane/`, signal: controller.signal })).then((loaded) => {
      if (controller.signal.aborted) return;
      installPluginTranslations(i18n, loaded.navigation);
      setPlugin(loaded);
    }, (reason: unknown) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason)); });
    return () => controller.abort();
  }, [connection.connection, connection.session?.id]);
  if (runtime instanceof BackplaneClient && !connection.session) {
    return <Login client={runtime} state={connection.connection} mode={mode} onThemeChange={setMode} />;
  }
  return <ThemeContext.Provider value={createGrafanaTheme(mode)}>
    {live ? <Suspense fallback={<p>{t('loading')}</p>}><Console key={connection.session?.id} mode={mode} onThemeChange={setMode} onLogout={async () => { if (runtime instanceof BackplaneClient) await runtime.logout(); }} /></Suspense> : <>
    <header><nav><Link to="/">{t('home')}</Link>{live && <Link to="/dev">{t('development')}</Link>}{plugin?.navigation.nav.map((item) => <Link key={item.path} to={`/s/hello/${item.path}`}>{i18n.t(item.labelKey, { ns: 'module.hello' })}</Link>)}</nav>
      <Button variant="outline" onClick={() => setMode(mode === 'dark' ? 'light' : 'dark')}>{t(mode === 'dark' ? 'light' : 'dark')}</Button>{runtime instanceof BackplaneClient && <Button onClick={() => { void runtime.logout(); }}>{t('logout')}</Button>}</header>
    <main><Routes>{live && <Route path="/dev" element={<Suspense fallback={<p>{t('loading')}</p>}><Development mode={mode} /></Suspense>} /> }<Route path="/" element={<h1>{t('title')}</h1>} /><Route path="/s/hello/*" element={error ? <div role="alert">{t('error')}<p>{error}</p></div> : !plugin ? <p role="status">{t('loading')}</p> :
      <PluginProvider context={{ service: 'hello', basePath: '/s/hello', mode, environment: 'embedded' }}><ErrorBoundary resetKey={plugin} fallback={() => <p role="alert">{t('error')}</p>}><plugin.Routes /></ErrorBoundary></PluginProvider>} /></Routes></main>
    </>}
  </ThemeContext.Provider>;
}
if (runtime instanceof BackplaneClient) void runtime.start();
createRoot(document.getElementById('root')!, errors && { onCaughtError: errors.onReactError, onUncaughtError: errors.onReactError }).render(<BackplaneProvider client={runtime}><I18nextProvider i18n={i18n}><BrowserRouter basename={consoleBase}><Host /></BrowserRouter></I18nextProvider></BackplaneProvider>);
