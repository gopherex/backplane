import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';
const origin = process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:10000', base = `${origin}/backplane`;
// Direct service traffic goes to Envoy, not the console-only Vite proxy.
const serviceOrigin = process.env.BACKPLANE_DEV_TARGET ?? origin;
const browser = await chromium.launch();
try {
  const context = await browser.newContext(), page = await context.newPage(), failures = [], sockets = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('websocket', (socket) => { if (new URL(socket.url()).pathname.endsWith('/ws')) sockets.push(socket.url()); });
  await page.goto(`${base}/s/hello/`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Hello module' })).toBeVisible();
  await expect(page.getByTestId('greetings')).toHaveText(/^\d+$/);
  const before = BigInt(await page.getByTestId('greetings').innerText());
  const greeting = await fetch(`${serviceOrigin}/hello/?name=BrowserAcceptance`);
  assert.equal(greeting.status, 200); assert.match(await greeting.text(), /Welcome, BrowserAcceptance!/);
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh statistics' }).click();
    assert.ok(BigInt(await page.getByTestId('greetings').innerText()) > before);
    assert.ok(BigInt(await page.getByTestId('audited').innerText()) > 0n);
  }).toPass({ timeout: 15000 });
  assert.equal(sockets.length, 1, 'platform and module clients must use one host socket');
  await page.goto(`${base}/dev`);
  await page.evaluate(() => { window.__backplaneWorkflowDocument = 'same-document'; });
  await page.getByRole('button', { name: 'Configuration', exact: true }).click();
  const override = page.getByRole('checkbox', { name: 'Override greeter.suffix', exact: true });
  await expect(override).toBeVisible();
  const hadOverride = await override.isChecked();
  const previousSuffix = hadOverride ? await page.getByRole('textbox', { name: 'greeter.suffix', exact: true }).innerText() : undefined;
  if (hadOverride) JSON.parse(previousSuffix);
  async function replaceSuffix(value) {
    const editor = page.getByRole('textbox', { name: 'greeter.suffix', exact: true });
    // Use CodeMirror's keymap instead of native contenteditable replacement.
    await editor.click();
    await editor.press('ControlOrMeta+A');
    await page.keyboard.insertText(value);
    await expect(editor).toHaveText(value);
  }
  if (!hadOverride) await override.check();
  const suffix = `b${Date.now().toString(36).slice(-6)}`;
  await replaceSuffix(JSON.stringify(suffix));
  await page.getByLabel('Comment', { exact: true }).fill(`acceptance ${suffix}`);
  await page.getByRole('button', { name: 'Validate', exact: true }).click();
  await expect(page.getByText('Validation passed.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
  const instance = page.getByRole('table', { name: 'Instances', exact: true }).locator('tbody tr').filter({ hasText: 'hello-dev' });
  const revisionRow = page.getByRole('table', { name: 'Revision history', exact: true }).locator('tbody tr').filter({ hasText: `acceptance ${suffix}` }).first();
  await expect(revisionRow).toBeVisible();
  const revision = await revisionRow.locator('td').first().innerText();
  await expect(instance.locator('td').nth(1)).toHaveText(revision, { timeout: 20000 });
  await expect(instance.locator('td').nth(2)).toHaveText('0');
  // Leave the development installation's prior override intact.
  if (hadOverride) await replaceSuffix(previousSuffix);
  else await override.uncheck();
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Binding', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toContainText('formatter.Format');
  await page.getByRole('button', { name: 'Operations', exact: true }).click();
  await page.getByLabel('Name', { exact: true }).selectOption('Greet');
  await page.getByRole('button', { name: 'Set value', exact: true }).click();
  await page.getByRole('textbox', { name: 'name *', exact: true }).fill('BrowserOperation');
  await page.getByRole('button', { name: 'Execute', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Output', exact: true })).toContainText('Welcome, BrowserOperation!', { timeout: 45000 });
  await page.getByRole('button', { name: 'Audit', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Audit', exact: true }).locator('tbody tr').first()).toBeVisible();
  await page.getByRole('button', { name: 'Explore', exact: true }).click();
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Logs', exact: true }).locator('tbody tr').first()).toBeVisible({ timeout: 20000 });
  await page.getByLabel('Signal', { exact: true }).selectOption('2');
  await page.getByRole('textbox', { name: 'Query', exact: true }).fill('{__name__!=""}');
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.locator('canvas').first()).toBeVisible({ timeout: 20000 });
  assert.equal(await page.evaluate(() => window.__backplaneWorkflowDocument), 'same-document', 'Lazy editors/charts must not reload the page');
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: 'Log out', exact: true }).click();
  await expect(page.getByLabel('Operator token')).toBeVisible();
  assert.deepEqual(failures, []);
  console.log('Live dev acceptance passed: service relay, config, binding, hook, audit, stored logs/metrics, both themes and logout');
} catch (error) { console.error(error); process.exitCode = 1; }
finally { await browser.close(); }
