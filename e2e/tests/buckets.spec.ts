import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('buckets management', () => {
  const name = `ui-bkt-${Date.now()}`;

  test.beforeEach(async ({ page }) => {
    await login(page);
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Buckets' }).click();
    await expect(page.getByRole('heading', { name: 'Buckets' })).toBeVisible();
  });

  // Best-effort cleanup so a failed run does not leave the bucket behind for
  // the next spec; a 404 here is the normal outcome after a green run.
  test.afterEach(async ({ page }) => {
    await page.request.delete(`/api/s3/${name}`, { headers: adminHeaders });
  });

  test('create, publish and delete a bucket through the dialogs', async ({ page }) => {
    const dialog = page.getByRole('dialog');
    const row = page.getByRole('row').filter({ hasText: name });

    // Create: the primary button stays disabled until the name is valid.
    await page.getByRole('button', { name: 'New bucket' }).click();
    await expect(dialog.getByRole('button', { name: 'Create bucket' })).toBeDisabled();
    await dialog.getByLabel('Name').fill(name);
    await dialog.getByRole('button', { name: 'Create bucket' }).click();
    await expect(dialog).toBeHidden();
    await expect(row).toBeVisible();
    await expect(row.getByRole('cell', { name: '0', exact: true })).toBeVisible();
    await expect(row.getByText('Private', { exact: true })).toBeVisible();

    // Filter narrows the table to the matching bucket.
    await page.getByLabel('Filter buckets').fill(name);
    await expect(page.getByRole('row')).toHaveCount(2); // header + match
    await page.getByLabel('Filter buckets').fill('');

    // Make public goes through a confirm; the badge flips on success.
    await row.getByRole('button', { name: 'Make public' }).click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Make public' }).click();
    await expect(dialog).toBeHidden();
    await expect(row.getByText('Public', { exact: true })).toBeVisible();
    await expect(row.getByRole('button', { name: 'Make private' })).toBeAttached();

    // Delete requires typing the bucket name before the action enables.
    await row.getByRole('button', { name: 'Delete bucket' }).click();
    await expect(dialog).toBeVisible();
    const confirm = dialog.getByRole('button', { name: 'Delete bucket' });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel('Type the name to confirm').fill(name);
    await confirm.click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('row').filter({ hasText: name })).toHaveCount(0);
  });
});
