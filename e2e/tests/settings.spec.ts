import { test, expect } from '@playwright/test';
import { login } from './fixtures';

test.describe('settings', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
    await page.getByRole('link', { name: 'Settings' }).click();
    await expect(page.getByRole('heading', { name: 'Rate limiting' })).toBeVisible();
  });

  test('durability (fsync) toggle round-trips with feedback', async ({ page }) => {
    // The checkbox is controlled by an async PUT, so click() + auto-waiting
    // assertions are used rather than check()/uncheck() (which assert state
    // synchronously and would race the re-render). The toggle is relative to
    // the current value so the test is robust to the persisted starting state
    // and restores it at the end.
    const fsync = page.getByLabel('Sync writes to disk (fsync)');
    const startOn = await fsync.isChecked();

    await fsync.click();
    await expect(fsync).toBeChecked({ checked: !startOn });
    await expect(page.getByText(startOn ? /Durable writes disabled/ : /Durable writes enabled/)).toBeVisible();

    // Toggle back to restore the original state and confirm the feedback flips.
    await fsync.click();
    await expect(fsync).toBeChecked({ checked: startOn });
    await expect(page.getByText(startOn ? /Durable writes enabled/ : /Durable writes disabled/)).toBeVisible();
  });

  test('metrics retention saves and reports the new window', async ({ page }) => {
    const days = page.getByLabel('Retention (days)');
    // Scope to this field's own Save button — the Access log section adds another.
    const save = days.locator('xpath=following::button[1]');

    await days.fill('14');
    await save.click();
    await expect(page.getByText('Request history retained for 14 days.')).toBeVisible();

    // Reset to the default so the setting is left clean.
    await days.fill('30');
    await save.click();
    await expect(page.getByText('Request history retained for 30 days.')).toBeVisible();
  });
});
