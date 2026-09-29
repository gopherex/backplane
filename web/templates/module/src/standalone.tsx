import 'virtual:backplane-grafana-assets';
import { useLayoutEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Routes, Route, Link } from 'react-router-dom';
import i18next from 'i18next';
import { initReactI18next, I18nextProvider, useTranslation } from 'react-i18next';
import { ThemeContext } from '@grafana/ui';
import { createGrafanaTheme, type ThemeMode } from '@gopherex/backplane-theme';
import { BackplaneProvider } from '@gopherex/backplane-react';
import { PluginProvider, installPluginTranslations } from '@gopherex/backplane-plugin-sdk';
import { Button, englishResources } from '@gopherex/backplane-ui';
import { fixtureClient } from './fixtures';
import navigation from './navigation';
import Embedded from './embedded';
import '@gopherex/backplane-ui/style.css';
import '@gopherex/backplane-theme/style.css';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import './standalone.css';

const i18n = i18next.createInstance();
await i18n.use(initReactI18next).init({ lng: 'en', fallbackLng: 'en', supportedLngs: ['en'], interpolation: { escapeValue: false }, resources: { en: { 'backplane.ui': englishResources } } });
installPluginTranslations(i18n, navigation);
function Standalone() {
  const { t } = useTranslation('module.hello');
  const [mode, setMode] = useState<ThemeMode>('dark');
  useLayoutEffect(() => { document.documentElement.dataset.theme = mode; document.documentElement.classList.toggle('dark', mode === 'dark'); }, [mode]);
  return <ThemeContext.Provider value={createGrafanaTheme(mode)}><BackplaneProvider client={fixtureClient}>
    <PluginProvider context={{ service: 'hello', basePath: '/', mode, environment: 'standalone' }}>
      <header><nav>{navigation.nav.map((item) => <Link key={item.path} to={`/${item.path}`}>{t(item.labelKey)}</Link>)}</nav><Button variant="outline" onClick={() => setMode(mode === 'dark' ? 'light' : 'dark')}>{t(mode === 'dark' ? 'light' : 'dark')}</Button></header>
      <main><Routes><Route path="/*" element={<Embedded />} /></Routes></main>
    </PluginProvider>
  </BackplaneProvider></ThemeContext.Provider>;
}
createRoot(document.getElementById('root')!).render(<I18nextProvider i18n={i18n}><BrowserRouter><Standalone /></BrowserRouter></I18nextProvider>);
