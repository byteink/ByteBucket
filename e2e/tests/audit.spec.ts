import { test, expect } from '@playwright/test';
import { login, adminHeaders } from './fixtures';

test.describe('audit log', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('records and displays a control-plane action', async ({ page }) => {
    // Seed an auditable mutation via the API, then confirm the UI shows it.
    const res = await page.request.post('/api/users', {
      headers: adminHeaders,
      data: { acl: [{ effect: 'Allow', buckets: ['audit-ui'], actions: ['*'] }] },
    });
    expect(res.ok()).toBeTruthy();
    const created = (await res.json()) as { accessKeyID: string };

    await page.getByRole('link', { name: 'Audit' }).click();
    await expect(page.getByRole('heading', { name: 'Audit log' })).toBeVisible();

    // The user.create event appears, targeting the new key.
    const row = page.getByRole('row').filter({ hasText: created.accessKeyID });
    await expect(row).toBeVisible();
    await expect(row.getByText('user.create')).toBeVisible();

    await page.request.delete(`/api/users/${created.accessKeyID}`, { headers: adminHeaders });
  });
});
