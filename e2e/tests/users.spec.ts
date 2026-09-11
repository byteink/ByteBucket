import { test, expect } from '@playwright/test';
import { login, ADMIN_AK } from './fixtures';

test.describe('users management', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Users' }).click();
    await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
  });

  test('the bootstrap admin is listed as admin', async ({ page }) => {
    const adminRow = page.getByRole('row').filter({ hasText: ADMIN_AK });
    await expect(adminRow).toBeVisible();
    await expect(adminRow.getByText('Admin', { exact: true })).toBeVisible();
    await expect(adminRow.getByText('All buckets, all actions')).toBeVisible();
    // The session key can never delete itself; the trash action is disabled.
    await expect(adminRow.getByRole('button', { name: 'You cannot delete your own key' })).toBeDisabled();
  });

  test('create, grant admin, then delete a user through the UI', async ({ page }) => {
    await page.getByRole('button', { name: 'New user' }).click();

    // The one-time secret dialog appears; capture the new access key from it.
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText('User created')).toBeVisible();
    const newAK = (await dialog.getByLabel('Access key ID', { exact: true }).inputValue()).trim();
    expect(newAK).not.toEqual('');
    await expect(dialog.getByLabel('Secret access key', { exact: true })).not.toHaveValue('');
    await dialog.getByRole('button', { name: 'Done' }).click();
    await expect(dialog).toBeHidden();

    // The new user appears in the table as a non-admin with an empty ACL.
    const row = page.getByRole('row').filter({ hasText: newAK });
    await expect(row).toBeVisible();
    await expect(row.getByText('User', { exact: true })).toBeVisible();
    await expect(row.getByText('No rules')).toBeVisible();

    // Promote through the access drawer: Role -> Admin, then save.
    await row.getByRole('button', { name: 'Edit access' }).click();
    const drawer = page.getByRole('dialog');
    await expect(drawer.getByText(newAK)).toBeVisible();
    await drawer.getByRole('button', { name: 'Admin', exact: true }).click();
    await drawer.getByRole('button', { name: 'Save access' }).click();
    await expect(drawer).toBeHidden();
    await expect(row.getByText('Admin', { exact: true })).toBeVisible();

    // Delete it; the confirm dialog names the action, not "OK".
    await row.getByRole('button', { name: 'Delete user' }).click();
    const confirm = page.getByRole('dialog');
    await expect(confirm.getByText(`Delete user ${newAK}?`)).toBeVisible();
    await confirm.getByRole('button', { name: 'Delete user' }).click();
    await expect(page.getByRole('row').filter({ hasText: newAK })).toHaveCount(0);
  });
});
