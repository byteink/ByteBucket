import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('dashboard', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('shows the overview header, stat tiles and refresh', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();

    // Scope to the tile grid: "Buckets" is also a nav link and "Objects" a
    // per-bucket column header once any bucket exists.
    const main = page.getByRole('main');
    const tiles = main.locator('.stat-grid');
    for (const label of ['Buckets', 'Objects', 'Storage used', 'Open multipart']) {
      await expect(tiles.getByText(label, { exact: true })).toBeVisible();
    }

    await expect(main.getByText(/Updated \d+s ago/)).toBeVisible();
    await page.getByRole('button', { name: 'Refresh now' }).click();
    await expect(main.getByText(/Updated \d+s ago/)).toBeVisible();
    await expect(tiles.getByText('Buckets', { exact: true })).toBeVisible();
  });

  test('per-bucket table lists a seeded bucket with its object count', async ({ page }) => {
    const bkt = 'dash-e2e-bkt';
    await page.request.put(`/api/s3/${bkt}`, { headers: adminHeaders });
    await page.request.put(`/api/s3/${bkt}/o.txt`, { headers: adminHeaders, data: 'hi' });
    await page.reload(); // the dashboard fetches stats on mount

    await expect(page.getByRole('heading', { name: 'Object activity' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Per bucket' })).toBeVisible();

    const row = page.getByRole('row', { name: new RegExp(bkt) });
    await expect(row).toBeVisible();
    await expect(row.getByRole('link', { name: bkt })).toHaveAttribute('href', `/buckets/${bkt}/objects`);
    // Objects is the third column (Bucket, Size, Objects); Uploads also reads
    // "1" after a single seed, so address the cell by position.
    await expect(row.getByRole('cell').nth(2)).toHaveText('1');
    // Hover action navigates to the bucket's object list.
    await row.hover();
    await row.getByRole('button', { name: 'Browse objects' }).click();
    await expect(page).toHaveURL(new RegExp(`/buckets/${bkt}/objects$`));

    await page.request.delete(`/api/s3/${bkt}/o.txt`, { headers: adminHeaders });
    await page.request.delete(`/api/s3/${bkt}`, { headers: adminHeaders });
  });

  test('request chart: range picker and window navigation', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Requests' })).toBeVisible();

    // All five ranges are selectable.
    const ranges = page.getByRole('group', { name: 'Range' });
    for (const r of ['1h', '24h', '7d', '14d', '30d']) {
      await expect(ranges.getByRole('button', { name: r, exact: true })).toBeVisible();
    }
    await expect(ranges.getByRole('button', { name: '24h', exact: true })).toHaveAttribute('aria-pressed', 'true');

    // At offset 0 forward navigation is disabled (no future); back is allowed.
    const forward = page.getByRole('button', { name: 'Later window' });
    const back = page.getByRole('button', { name: 'Earlier window' });
    await expect(forward).toBeDisabled();
    await expect(back).toBeEnabled();

    // Stepping back enables forward; the window label updates.
    const label = page.locator('span.tabular-nums').first();
    await expect(label).not.toHaveText(/^\s*$/);
    const labelBefore = await label.textContent();
    await back.click();
    await expect(forward).toBeEnabled();
    await expect(label).not.toHaveText(labelBefore ?? '');

    // Switching range resets to "now" — forward disabled again.
    await ranges.getByRole('button', { name: '1h', exact: true }).click();
    await expect(forward).toBeDisabled();
    await expect(ranges.getByRole('button', { name: '1h', exact: true })).toHaveAttribute('aria-pressed', 'true');
    // The chart renders one column per minute in the 1h window.
    await expect(page.locator('.plot .bar')).toHaveCount(60);
  });
});
