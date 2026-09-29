import { defineConfig } from '@playwright/test';
import { join } from 'node:path';
const directory = process.env.BACKPLANE_PACKED_DIR;
if (!directory) throw new Error('Run yarn test:packed to prepare external consumers');
export default defineConfig({
  testDir: './tests', testMatch: ['standalone.spec.ts', 'embedding.spec.ts'], workers: 2,
  use: { viewport: { width: 1280, height: 900 } },
  webServer: [
    { command: process.env.BACKPLANE_PACKED_DEV ? 'yarn dev --port 4179 --strictPort' : 'yarn preview --port 4179 --strictPort', cwd: join(directory, 'module'), url: 'http://127.0.0.1:4179', timeout: 30000 },
    { command: 'node scripts/serve-embedding.mjs', url: 'http://127.0.0.1:4180/backplane/', timeout: 30000,
      env: { BACKPLANE_PLUGIN_DIR: join(directory, 'module/dist/plugin'), BACKPLANE_HOST_DIR: join(directory, 'host/dist') } },
  ],
});
