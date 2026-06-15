import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('logs', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('control tab shows a control-plane action', async ({ page }) => {
    // Seed an auditable mutation via the API, then confirm the Control tab shows it.
    const res = await page.request.post('/api/users', {
      headers: adminHeaders,
      data: { acl: [{ effect: 'Allow', buckets: ['logs-ui'], actions: ['*'] }] },
    });
    expect(res.ok()).toBeTruthy();
    const created = (await res.json()) as { accessKeyID: string };

    await page.getByRole('link', { name: 'Logs' }).click();
    await expect(page.getByRole('heading', { name: 'Logs' })).toBeVisible();
    await page.getByRole('button', { name: 'Control' }).click();

    const row = page.getByRole('row').filter({ hasText: created.accessKeyID });
    await expect(row).toBeVisible();
    await expect(row.getByText('user.create')).toBeVisible();

    await page.request.delete(`/api/users/${created.accessKeyID}`, { headers: adminHeaders });
  });

  test('access tab shows a data-plane object access', async ({ page }) => {
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

    await page.getByRole('link', { name: 'Logs' }).click();
    // Access is the default tab. The flusher batches off the request path, so
    // reload-poll until the PutObject event surfaces.
    await expect(async () => {
      await page.reload();
      await expect(page.getByRole('row').filter({ hasText: key })).toBeVisible({ timeout: 1500 });
    }).toPass({ timeout: 10000 });
    await expect(
      page.getByRole('row').filter({ hasText: key }).getByText('PutObject'),
    ).toBeVisible();

    // Cleanup: remove the object/bucket and turn logging back off.
    await page.request.delete(`/api/s3/${bucket}/${key}`, { headers: adminHeaders });
    await page.request.delete(`/api/s3/${bucket}`, { headers: adminHeaders });
    await page.request.put('/api/config/accesslog', {
      headers: adminHeaders,
      data: { enabled: false, maxEvents: 100000, maxAgeDays: 30 },
    });
  });
});
