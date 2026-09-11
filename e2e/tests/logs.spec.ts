import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('logs', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('control category shows a control-plane action with details', async ({ page }) => {
    // Seed an auditable mutation via the API, then confirm the Control view shows it.
    const res = await page.request.post('/api/users', {
      headers: adminHeaders,
      data: { acl: [{ effect: 'Allow', buckets: ['logs-ui'], actions: ['*'] }] },
    });
    expect(res.ok()).toBeTruthy();
    const created = (await res.json()) as { accessKeyID: string };

    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Logs' }).click();
    await expect(page.getByRole('heading', { name: 'Logs' })).toBeVisible();
    await page.getByRole('button', { name: 'Control' }).click();

    const row = page.getByRole('row').filter({ hasText: created.accessKeyID });
    await expect(row).toBeVisible();
    await expect(row.getByText('user.create')).toBeVisible();

    // The search box narrows the loaded rows client-side.
    const search = page.getByLabel('Filter by target, actor or action');
    await search.fill('no-such-target-zzz');
    await expect(row).toBeHidden();
    await expect(page.getByText(/Filtering 0 of \d+ loaded events/)).toBeVisible();
    await search.fill(created.accessKeyID);
    await expect(row).toBeVisible();

    // The row action opens the details dialog for that event.
    await row.getByRole('button', { name: 'Request details' }).click();
    const dialog = page.getByRole('dialog', { name: 'Request details' });
    await expect(dialog.getByText('user.create', { exact: true })).toBeVisible();
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    await page.request.delete(`/api/users/${created.accessKeyID}`, { headers: adminHeaders });
  });

  test('access category shows a data-plane object access with filters', async ({ page }) => {
    // Enable access logging, then drive an object write through the admin S3
    // surface (same AccessLog middleware as port 9000).
    const cfg = await page.request.put('/api/config/accesslog', {
      headers: adminHeaders,
      data: { enabled: true, maxEvents: 1000, maxAgeDays: 30 },
    });
    expect(cfg.ok()).toBeTruthy();

    const bucket = 'logs-ui-data';
    const key = 'ui-probe.txt';
    await page.request.put(`/api/s3/${bucket}`, { headers: adminHeaders });
    const put = await page.request.put(`/api/s3/${bucket}/${key}`, {
      headers: adminHeaders,
      data: 'hello access log',
    });
    expect(put.ok()).toBeTruthy();

    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Logs' }).click();
    // Access is the default category. The flusher batches off the request
    // path, so reload-poll until the PutObject event surfaces.
    // Scope to the PutObject row: a previous run's cleanup leaves a
    // DeleteObject event for the same key when the suite reuses a volume.
    const row = page.getByRole('row').filter({ hasText: key }).filter({ hasText: 'PutObject' });
    await expect(async () => {
      await page.reload();
      await expect(row).toBeVisible({ timeout: 1500 });
    }).toPass({ timeout: 10000 });

    // Bucket and operation selects are populated from the loaded events and
    // filter client-side; a non-matching status class empties the table.
    await page.getByLabel('Bucket', { exact: true }).selectOption(bucket);
    await page.getByLabel('Operation', { exact: true }).selectOption('PutObject');
    await expect(row).toBeVisible();
    await page.getByLabel('Status', { exact: true }).selectOption('5xx');
    await expect(row).toBeHidden();
    await expect(page.getByText(/Filtering 0 of \d+ loaded events/)).toBeVisible();
    await page.getByLabel('Status', { exact: true }).selectOption('2xx');
    await expect(row).toBeVisible();

    // The row action opens the details dialog carrying the request envelope.
    await row.getByRole('button', { name: 'Request details' }).click();
    const dialog = page.getByRole('dialog', { name: 'Request details' });
    await expect(dialog.getByText(key, { exact: true })).toBeVisible();
    await expect(dialog.getByText('User agent')).toBeVisible();
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    // Cleanup: remove the object/bucket and turn logging back off.
    await page.request.delete(`/api/s3/${bucket}/${key}`, { headers: adminHeaders });
    await page.request.delete(`/api/s3/${bucket}`, { headers: adminHeaders });
    await page.request.put('/api/config/accesslog', {
      headers: adminHeaders,
      data: { enabled: false, maxEvents: 100000, maxAgeDays: 30 },
    });
  });
});
