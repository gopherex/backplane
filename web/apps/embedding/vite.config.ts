import { defineConfig } from 'vite';
import { platformHostBuild } from '@gopherex/backplane-plugin-build/federation';
import { consoleDevServer } from './dev-proxy';
export default defineConfig(({ mode, command }) => {
  const base = process.env.BACKPLANE_CONSOLE_BASE ?? '/backplane/';
  const config = platformHostBuild({ base });
  if (mode === 'live') config.plugins!.push({ name: 'live-installation', transformIndexHtml: () => [{ tag: 'meta', attrs: { name: 'backplane-fixture', content: 'live' }, injectTo: 'head' }] });
  if (mode === 'live' && command === 'serve') config.server = consoleDevServer(base, process.env.BACKPLANE_DEV_TARGET);
  return config;
});
