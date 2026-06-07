import { test, expect } from '@playwright/test';
import { login, ADMIN_AK } from './fixtures';

test.describe('users management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
    await page.getByRole('link', { name: 'Users' }).click();
    await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
  });

  test('the bootstrap admin is listed as admin', async ({ page }) => {
    const adminRow = page.getByRole('row').filter({ hasText: ADMIN_AK });
    await expect(adminRow).toBeVisible();
    await expect(adminRow.getByText('admin', { exact: true })).toBeVisible();
  });

  test('create then delete a user through the UI', async ({ page }) => {
    await page.getByRole('button', { name: 'New user' }).click();

    // The one-time secret modal appears; capture the new access key from it.
    const modal = page.locator('div.fixed');
    await expect(modal.getByText('User created')).toBeVisible();
    const newAK = (await modal.locator('code').first().innerText()).trim();
    expect(newAK).not.toEqual('');
    await modal.getByRole('button', { name: 'Done' }).click();

    // The new user appears in the table as a non-admin (empty ACL).
    const row = page.getByRole('row').filter({ hasText: newAK });
    await expect(row).toBeVisible();
    await expect(row.getByText('user', { exact: true })).toBeVisible();

    // Delete it; the confirm dialog must be accepted.
    page.once('dialog', (d) => d.accept());
    await row.getByRole('button', { name: 'Delete' }).click();
    await expect(page.getByRole('row').filter({ hasText: newAK })).toHaveCount(0);
  });
});
