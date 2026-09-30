import { defineConfig } from 'vite';
import { platformHostBuild } from '@gopherex/backplane-plugin-build/federation';
import tailwind from '@tailwindcss/vite';
import { consoleDevServer } from './dev-proxy';
export default defineConfig(({ mode, command }) => {
  const base = process.env.BACKPLANE_CONSOLE_BASE ?? '/backplane/';
  const config = platformHostBuild({ base });
  config.plugins!.push(tailwind());
  if (mode === 'live') config.plugins!.push({ name: 'live-installation', transformIndexHtml: () => [{ tag: 'meta', attrs: { name: 'backplane-fixture', content: 'live' }, injectTo: 'head' }] });
  if (mode === 'live' && command === 'serve') {
    config.server = consoleDevServer(base, process.env.BACKPLANE_DEV_TARGET);
    // These workspace-kit chunks are lazy and invisible to the initial scan.
    // Discovering them on navigation would reload the page and discard drafts.
    config.optimizeDeps = { include: [
      '@codemirror/state', '@codemirror/view', '@codemirror/language',
      '@codemirror/lang-json', '@codemirror/commands', '@codemirror/autocomplete',
      '@codemirror/lint', '@codemirror/merge', '@lezer/highlight',
      'uplot', '@grafana/schema',
    ] };
  }
  return config;
});
