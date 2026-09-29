import { defineConfig } from '@playwright/test';
if (!process.env.MODULE_TEST_URL) throw new Error('Set MODULE_TEST_URL to an already running author dev server or container');
export default defineConfig({ testDir: './tests', testMatch: 'standalone.spec.ts', use: { viewport: { width: 1280, height: 900 } } });
