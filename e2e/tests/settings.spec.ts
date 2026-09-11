import { test, expect, type Page } from '@playwright/test';
import { login } from './fixtures';

// section scopes a locator to one settings block so its Save button is not
// confused with the other sections' Save buttons.
function section(page: Page, title: string) {
  return page.getByRole('heading', { name: title }).locator('xpath=ancestor::section[1]');
}

test.describe('settings', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Settings' }).click();
    await expect(page.getByRole('heading', { name: 'Rate limiting' })).toBeVisible();
  });

  test('durability (fsync) saves explicitly with feedback', async ({ page }) => {
    // The toggle is relative to the current value so the test is robust to
    // the persisted starting state and restores it at the end. Nothing is
    // written until Save is clicked.
    const durability = section(page, 'Durability');
    const fsync = page.getByLabel('Sync writes to disk (fsync)');
    const save = durability.getByRole('button', { name: 'Save' });
    const startOn = await fsync.isChecked();

    await expect(save).toBeDisabled();
    await fsync.click();
    await expect(fsync).toBeChecked({ checked: !startOn });
    await save.click();
    await expect(page.getByText(startOn ? /Durable writes disabled/ : /Durable writes enabled/)).toBeVisible();
    await expect(save).toBeDisabled();

    // Toggle back to restore the original state and confirm the feedback flips.
    await fsync.click();
    await expect(fsync).toBeChecked({ checked: startOn });
    await save.click();
    await expect(page.getByText(startOn ? /Durable writes enabled/ : /Durable writes disabled/)).toBeVisible();
  });

  test('metrics retention saves and reports the new window', async ({ page }) => {
    const retention = section(page, 'Metrics retention');
    const days = page.getByLabel('Retention (days)');
    const save = retention.getByRole('button', { name: 'Save' });

    await days.fill('14');
    await save.click();
    await expect(page.getByText('Request history retained for 14 days.')).toBeVisible();

    // Reset to the default so the setting is left clean.
    await days.fill('30');
    await save.click();
    await expect(page.getByText('Request history retained for 30 days.')).toBeVisible();
  });

  test('access log settings round-trip with feedback', async ({ page }) => {
    const accessLog = section(page, 'Access log');
    const enabled = page.getByLabel('Record object access');
    const maxEvents = page.getByLabel('Max events');
    const save = accessLog.getByRole('button', { name: 'Save' });
    const startOn = await enabled.isChecked();
    const startMax = await maxEvents.inputValue();

    await enabled.click();
    await maxEvents.fill('12345');
    await save.click();
    await expect(accessLog.getByRole('status')).toHaveText('Saved');
    await expect(enabled).toBeChecked({ checked: !startOn });
    await expect(maxEvents).toHaveValue('12345');

    // Restore the persisted values so later specs start from the same state.
    await enabled.click();
    await maxEvents.fill(startMax);
    await save.click();
    await expect(accessLog.getByRole('status')).toHaveText('Saved');
    await expect(enabled).toBeChecked({ checked: startOn });
  });
});
