import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
for (const theme of ['dark', 'light']) for (const story of ['services-and-health', 'configuration', 'audit', 'explore', 'operations', 'bindings', 'rules', 'workflows', 'schedules', 'events-and-dead-letters']) {
  test(`${theme}: platform ${story}`, async ({ page }) => {
    const errors: string[] = []; page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`/iframe.html?id=kit-platform--${story}&viewMode=story&globals=theme:${theme}`);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    if (story === 'services-and-health') {
      const table = page.getByRole('table', { name: 'Instances', exact: true }); await expect(table.getByRole('cell', { name: 'hello-1', exact: true })).toBeVisible();
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      await expect(page.getByRole('cell', { name: 'Waiting for database', exact: true })).toBeVisible();
      await expect(page.getByRole('textbox', { name: 'Effective configuration', exact: true })).toContainText('***');
    }
    if (story === 'configuration') {
      const editor = page.getByRole('textbox', { name: 'greeter.suffix', exact: true }); await expect(editor).toContainText('!');
      await editor.fill('"changed"'); await page.getByRole('button', { name: 'Reconnect fixture' }).click(); await expect(editor).toContainText('changed'); await page.getByRole('checkbox', { name: 'Simulate write failure' }).check(); await page.getByRole('button', { name: 'Save', exact: true }).click();
      await expect(page.getByText('The action did not complete successfully. Check its outcome before retrying.')).toBeVisible(); await expect(editor).toContainText('changed');
      await expect(page.getByLabel('Write attempts')).toHaveText('1'); await page.waitForTimeout(350); await expect(page.getByLabel('Write attempts')).toHaveText('1');
      await page.getByRole('checkbox', { name: 'Simulate write failure' }).uncheck(); await page.getByRole('button', { name: 'Validate', exact: true }).click();
      await expect(page.getByText('Validation passed.')).toBeVisible(); await page.getByRole('button', { name: 'Save', exact: true }).click(); await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
      await expect(page.getByLabel('Write attempts')).toHaveText('2');
    }
    if (story === 'audit') {
      const table = page.getByRole('table', { name: 'Audit', exact: true }); await expect(table.getByRole('cell', { name: '9007199254740994', exact: true })).toBeVisible();
      await expect(page.getByLabel('Watch cursors')).toHaveText('snapshot'); await page.getByRole('button', { name: 'Reconnect fixture' }).click();
      await expect(page.getByLabel('Watch cursors')).toContainText('empty-advanced'); await expect(table.locator('tbody tr')).toHaveCount(2);
      await page.getByRole('button', { name: 'Expire audit cursor' }).click(); await expect(page.getByText('The audit cursor has expired. Reload to establish a new snapshot.')).toBeVisible();
      await page.getByRole('button', { name: 'Refresh', exact: true }).click(); await expect(page.getByText('The audit cursor has expired. Reload to establish a new snapshot.')).toHaveCount(0);
      await page.getByRole('button', { name: 'Replace session' }).click(); await expect(table.locator('tbody tr')).toHaveCount(0); await expect(page.getByLabel('Active watches')).toHaveText('0');
    }
    if (story === 'explore') {
      await expect(page.getByRole('cell', { name: '{"service.name":"kratos"}', exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Run query' }).click(); await expect(page.getByRole('cell', { name: 'Hello from storage', exact: true })).toBeVisible();
      await page.getByLabel('Signal', { exact: true }).selectOption('3'); await page.getByLabel('Query language', { exact: true }).selectOption('traceql');
      await page.getByRole('button', { name: 'Run query' }).click(); const traces = page.getByRole('table', { name: 'Traces', exact: true }); await expect(traces.getByRole('cell', { name: 'Greet', exact: true })).toBeVisible();
      await traces.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      await expect(page.getByRole('table', { name: '1234567890abcdef1234567890abcdef', exact: true })).toBeVisible();
    }
    if (story === 'operations') {
      await page.getByLabel('Name', { exact: true }).selectOption('Greet'); await expect(page.getByLabel('Sequence', { exact: true })).toHaveValue('18446744073709551615');
      await page.getByRole('button', { name: 'Execute', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('{"name":"World","sequence":18446744073709551615}');
      await expect(page.getByRole('textbox', { name: 'Output', exact: true })).toContainText('hook/hello/Greet/fixture');
    }
    if (story === 'bindings' || story === 'rules') {
      await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toBeVisible();
      if (story === 'bindings') {
        const definition = page.getByRole('textbox', { name: 'Definition', exact: true });
        await definition.fill('{"hook":"hello.Greet","steps":[]}');
        await page.getByRole('button', { name: 'Reconnect fixture' }).click();
        await expect(definition).toHaveText('{"hook":"hello.Greet","steps":[]}');
        await page.getByRole('button', { name: 'Validate', exact: true }).click();
        await expect(page.getByRole('alert')).toContainText('At least one step is required');
        await expect(page.getByLabel('Write attempts')).toHaveText('0');
        await definition.fill('{"hook":"hello.Greet","steps":[{"name":"format","activity":"formatter.Format"}]}');
      }
      if (story === 'rules') await page.getByLabel('Name', { exact: true }).fill('Greeted rule');
      await page.getByRole('button', { name: 'Validate', exact: true }).click(); await expect(page.getByText('Validation passed.')).toBeVisible();
      await page.getByRole('button', { name: 'Save', exact: true }).click(); await expect(page.getByText('Saved: 2', { exact: true })).toBeVisible(); await expect(page.getByLabel('Write attempts')).toHaveText('1');
    }
    if (story === 'workflows') {
      const table = page.getByRole('table', { name: 'Runs', exact: true }); await expect(table.getByRole('cell', { name: '9007199254740993', exact: true })).toBeVisible();
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter'); await expect(page.getByText('DatabaseError: Activity failed', { exact: true })).toBeVisible();
      await page.locator('summary').filter({ hasText: /^Cancel run$/ }).click(); await page.getByRole('button', { name: 'Cancel run', exact: true }).click(); await page.getByRole('button', { name: 'Confirm', exact: true }).click();
      await expect(page.getByLabel('Write attempts')).toHaveText('1');
    }
    if (story === 'schedules') {
      const table = page.getByRole('table', { name: 'Schedules', exact: true }); await expect(table.getByRole('cell', { name: 'Welcome', exact: true })).toBeVisible(); await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      await page.getByRole('button', { name: 'Trigger', exact: true }).click(); await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Write attempts')).toHaveText('1');
    }
    if (story === 'events-and-dead-letters') {
      await expect(page.getByRole('button', { name: 'Purge selected', exact: true })).toBeDisabled();
      await page.getByRole('checkbox', { name: 'Select row 9007199254740993', exact: true }).check(); await page.getByRole('button', { name: 'Redrive selected', exact: true }).click();
      await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('9007199254740993'); await expect(page.getByLabel('Write attempts')).toHaveText('1');
    }
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByRole('alertdialog')).toHaveCount(0);
    expect((await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
    expect(errors).toEqual([]);
  });
}
