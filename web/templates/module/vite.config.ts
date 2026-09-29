import { defineConfig } from 'vite';
import { moduleBuild } from '@gopherex/backplane-plugin-build/federation';
import metadata from './plugin.config.json' with { type: 'json' };
export default defineConfig(({ mode }) => moduleBuild({ ...metadata, embedded: mode === 'embedded' }));
