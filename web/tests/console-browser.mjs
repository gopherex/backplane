import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const base = `${process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:10000'}/backplane`;
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 950 } });
  const page = await context.newPage(), errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`${base}/`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page).toHaveURL(`${base}/services`);
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  const sidebar = page.getByRole('complementary', { name: 'Main navigation' });
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toBeVisible();
  assert.deepEqual(await sidebar.getByRole('navigation', { name: 'Platform', exact: true }).getByRole('link').allTextContents(), ['Services', 'Explore', 'Audit']);
  await page.screenshot({ path: '/tmp/backplane-console-dark.png' });
  assert.deepEqual((await new AxeBuilder({ page }).include('.console-shell').withTags(['wcag2a', 'wcag2aa']).analyze()).violations, []);

  // Width can be changed by keyboard or pointer, and survives reload.
  const resize = sidebar.getByRole('separator', { name: 'Navigation width' });
  await resize.focus(); await page.keyboard.press('ArrowRight');
  await expect(resize).toHaveAttribute('aria-valuenow', '280');
  const box = await resize.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + 80); await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + 40, box.y + 80); await page.mouse.up();
  await expect(resize).toHaveAttribute('aria-valuenow', '320');
  await page.reload(); await expect(resize).toHaveAttribute('aria-valuenow', '320');
  await resize.focus(); await page.keyboard.press('Home'); await expect(resize).toHaveAttribute('aria-valuenow', '224');
  await resize.dblclick(); await expect(resize).toHaveAttribute('aria-valuenow', '264');

  await sidebar.getByRole('button', { name: 'Collapse navigation' }).click();
  await page.mouse.move(900, 400);
  await expect(sidebar).toHaveAttribute('data-expanded', 'false');
  assert.equal((await sidebar.boundingBox()).width, 56);
  await page.reload(); await expect(sidebar).toHaveAttribute('data-expanded', 'false');
  await sidebar.hover(); await expect(sidebar).toHaveAttribute('data-expanded', 'true');
  await sidebar.getByRole('button', { name: 'Pin navigation' }).click();
  await page.mouse.move(900, 400); await expect(sidebar).toHaveAttribute('data-expanded', 'true');
  await sidebar.getByRole('button', { name: 'hello', exact: true }).click();
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toHaveCount(0);
  await sidebar.getByRole('button', { name: 'hello', exact: true }).click();
  await sidebar.getByRole('link', { name: 'Settings', exact: true }).click();
  await expect(page).toHaveURL(`${base}/s/hello/settings`);
  await page.getByLabel('Display name').fill('Navigation check');
  await page.reload(); await expect(page.getByLabel('Display name')).toBeVisible();
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toHaveAttribute('aria-current', 'page');
  await sidebar.getByRole('link', { name: 'Manage hello' }).click();
  await expect(page).toHaveURL(`${base}/services/hello`);
  await expect(page.getByRole('table', { name: 'Instances', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'Configuration', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Override greeter.suffix', exact: true })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Explore', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Query', exact: true })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Audit', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Audit', exact: true })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Services', exact: true }).click();
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await page.reload(); await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toBeVisible();
  await page.screenshot({ path: '/tmp/backplane-console-light.png' });
  assert.deepEqual((await new AxeBuilder({ page }).include('.console-shell').withTags(['wcag2a', 'wcag2aa']).analyze()).violations, []);

  // Mobile navigation overlays the content, traps keyboard focus, and closes on navigation.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(sidebar).toBeHidden();
  await page.getByRole('button', { name: 'Open navigation' }).click();
  await expect(sidebar).toBeVisible();
  await expect(sidebar.getByRole('link', { name: 'Services', exact: true })).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(sidebar.getByRole('button', { name: 'Collapse navigation' })).toBeFocused();
  await page.screenshot({ path: '/tmp/backplane-console-mobile.png' });
  await page.keyboard.press('Escape'); await expect(sidebar).toBeHidden();
  await page.getByRole('button', { name: 'Open navigation' }).click();
  await sidebar.getByRole('link', { name: 'Settings', exact: true }).click();
  await expect(sidebar).toBeHidden(); await expect(page.getByLabel('Display name')).toBeVisible();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  // A rejected bundle cannot break platform navigation or another route.
  await page.route('**/plugins/hello/*/plugin.json', (route) => route.fulfill({ json: { name: 'hello', sdk_major: 999, nav: [] } }));
  await page.reload();
  await expect(page.getByRole('alert')).toContainText('Plugin metadata does not match the catalog');
  await page.getByRole('button', { name: 'Open navigation' }).click();
  await sidebar.getByRole('link', { name: 'Services', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  await page.unroute('**/plugins/hello/*/plugin.json');
  // Failed logout keeps the shell/session and exposes an explicit retry.
  await page.route('**/auth/logout', (route) => route.fulfill({ status: 503, body: 'Unavailable' }));
  await page.getByRole('button', { name: 'Log out', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Could not log out. Your session is still active.');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  await page.unroute('**/auth/logout');
  await page.getByRole('button', { name: 'Log out', exact: true }).click();
  await expect(page.getByLabel('Operator token')).toBeVisible();
  assert.deepEqual(errors, []);
  console.log('Console acceptance passed: live registry, routes, module pages, resize, collapse, persistence, themes, keyboard/mobile and accessibility');
} finally { await browser.close(); }
