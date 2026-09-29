import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests', testMatch: '*storybook.spec.ts', workers: 2,
  use: { baseURL: 'http://127.0.0.1:4181', viewport: { width: 1280, height: 960 } },
  webServer: { command: 'yarn vite preview --outDir apps/catalog/storybook-static --host 127.0.0.1 --port 4181 --strictPort', url: 'http://127.0.0.1:4181', reuseExistingServer: false },
});
