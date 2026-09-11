import { test, expect, type Page } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

const bucket = `ui-obj-${Date.now()}`;
const key = 'hello.txt';
const body = 'hello from the object browser';

async function seedObject(page: Page): Promise<void> {
  const put = await page.request.put(`/api/s3/${bucket}/${key}`, {
    headers: { ...adminHeaders, 'Content-Type': 'text/plain' },
    data: body,
  });
  expect(put.ok()).toBeTruthy();
}

test.describe('objects', () => {
  test.beforeAll(async ({ request }) => {
    const res = await request.put(`/api/s3/${bucket}`, { headers: adminHeaders });
    expect(res.ok()).toBeTruthy();
  });

  // Best-effort teardown so a failed run does not leave state behind; 404s
  // here are the normal outcome after a green run.
  test.afterAll(async ({ request }) => {
    await request.delete(`/api/s3/${bucket}/${key}`, { headers: adminHeaders });
    await request.delete(`/api/s3/${bucket}`, { headers: adminHeaders });
  });

  test.beforeEach(async ({ page }) => {
    await seedObject(page);
    await login(page);
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Buckets' }).click();
    await page.getByRole('link', { name: bucket, exact: true }).click();
    await expect(page.getByRole('heading', { name: bucket })).toBeVisible();
  });

  test('lists an object and deletes it through the selection bar', async ({ page }) => {
    const row = page.getByRole('row').filter({ hasText: key });
    await expect(row).toBeVisible();
    await expect(row.getByText('Private', { exact: true })).toBeVisible();

    // Filter narrows the table; a miss leaves only the header row.
    await page.getByLabel('Filter this folder').fill('no-such-object');
    await expect(page.getByRole('row')).toHaveCount(1);
    await page.getByLabel('Filter this folder').fill('');

    await page.getByLabel(`Select ${key}`).check();
    const bar = page.getByRole('toolbar', { name: 'Selection' });
    await expect(bar.getByText('1 selected')).toBeVisible();
    await bar.getByRole('button', { name: 'Delete' }).click();

    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Delete object' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('row').filter({ hasText: key })).toHaveCount(0);
    await expect(page.getByText('Empty bucket.')).toBeVisible();
  });

  test('detail page shows metadata and a text preview', async ({ page }) => {
    await page.getByRole('link', { name: key, exact: true }).click();
    await expect(page.getByRole('heading', { name: key })).toBeVisible();

    const details = page.locator('dl');
    await expect(details.getByText(key, { exact: true })).toBeVisible();
    await expect(details.getByText('text/plain')).toBeVisible();
    await expect(page.locator('pre')).toHaveText(body);

    // The share input is readonly and carries the public URL for this key.
    await expect(page.getByLabel('Public URL', { exact: true })).toHaveValue(new RegExp(`/${bucket}/${key}$`));

    // Delete from the detail page returns to the folder listing.
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByRole('button', { name: 'Delete object' }).click();
    await expect(page).toHaveURL(new RegExp(`/buckets/${bucket}/objects$`));
    await expect(page.getByText('Empty bucket.')).toBeVisible();
  });
});
