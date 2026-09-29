import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  testIgnore: '*storybook.spec.ts',
  use: { baseURL: 'http://127.0.0.1:4178', viewport: { width: 1280, height: 900 } },
  webServer: [{
    command: 'yarn workspace @backplane/catalog preview --port 4178 --strictPort',
    url: 'http://127.0.0.1:4178',
    reuseExistingServer: false,
    timeout: 30_000,
  }, {
    command: 'yarn workspace @backplane/module-template preview --port 4179 --strictPort',
    url: 'http://127.0.0.1:4179',
    reuseExistingServer: false,
    timeout: 30_000,
  }, {
    command: 'node scripts/serve-embedding.mjs',
    url: 'http://127.0.0.1:4180/backplane/',
    reuseExistingServer: false,
    timeout: 30_000,
  }],
});
