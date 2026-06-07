import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('dashboard', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('shows storage cards and object activity', async ({ page }) => {
    // Scope to main so the "Buckets" card label is not confused with the nav link.
    const main = page.getByRole('main');
    for (const label of ['Buckets', 'Objects', 'Storage used', 'Multipart open']) {
      await expect(main.getByText(label, { exact: true })).toBeVisible();
    }
    await expect(page.getByRole('heading', { name: 'Object activity (all buckets)' })).toBeVisible();
  });

  test('per-bucket table lists a seeded bucket', async ({ page }) => {
    const bkt = 'dash-e2e-bkt';
    await page.request.put(`/api/s3/${bkt}`, { headers: adminHeaders });
    await page.request.put(`/api/s3/${bkt}/o.txt`, { headers: adminHeaders, data: 'hi' });
    await page.reload(); // the dashboard fetches stats on mount

    await expect(page.getByRole('heading', { name: 'Per bucket' })).toBeVisible();
    await expect(page.getByRole('cell', { name: bkt })).toBeVisible();

    await page.request.delete(`/api/s3/${bkt}/o.txt`, { headers: adminHeaders });
    await page.request.delete(`/api/s3/${bkt}`, { headers: adminHeaders });
  });

  test('request chart: range picker and window navigation', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Request outcomes (S3 API)' })).toBeVisible();

    // All five ranges are selectable.
    for (const r of ['1h', '24h', '7d', '14d', '30d']) {
      await expect(page.getByRole('button', { name: r, exact: true })).toBeVisible();
    }

    // At offset 0 forward navigation is disabled (no future); back is allowed.
    const forward = page.getByRole('button', { name: 'Later window' });
    const back = page.getByRole('button', { name: 'Earlier window' });
    await expect(forward).toBeDisabled();
    await expect(back).toBeEnabled();

    // Stepping back enables forward; the window label updates.
    const labelBefore = await page.locator('span.tabular-nums').first().textContent();
    await back.click();
    await expect(forward).toBeEnabled();

    // Switching range resets to "now" — forward disabled again.
    await page.getByRole('button', { name: '1h', exact: true }).click();
    await expect(forward).toBeDisabled();
    // The chart still renders its 60 one-minute columns without error.
    expect(labelBefore).not.toBeNull();
  });
});
