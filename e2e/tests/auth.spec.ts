import { test, expect } from '@playwright/test';
import { login, ADMIN_AK, ADMIN_SK } from './fixtures';

test.describe('authentication', () => {
  test('unauthenticated visit redirects to login', async ({ page }) => {
    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/login$/);
    await expect(page.getByRole('heading', { name: 'ByteBucket' })).toBeVisible();
  });

  test('bad credentials show an error and stay on login', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Access key ID').fill('wrong');
    await page.getByLabel('Secret access key').fill('wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByText('Invalid admin credentials')).toBeVisible();
    await expect(page).toHaveURL(/\/login$/);
  });

  test('valid credentials land on the overview', async ({ page }) => {
    await login(page);
    await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
    // The sidebar shows the authenticated access key.
    await expect(page.getByText(ADMIN_AK)).toBeVisible();
  });

  test('logout returns to login', async ({ page }) => {
    await login(page);
    await page.getByRole('button', { name: 'Log out' }).click();
    await expect(page).toHaveURL(/\/login$/);
    // Session cleared: the guard blocks a direct dashboard visit again.
    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/login$/);
  });

  test('session cookie is HttpOnly, SameSite=Strict and scoped to the API', async ({ page, context }) => {
    await login(page);
    const cookie = (await context.cookies()).find((c) => c.name === 'bb_admin_session');
    expect(cookie).toBeDefined();
    expect(cookie?.httpOnly).toBe(true);
    expect(cookie?.sameSite).toBe('Strict');
    expect(cookie?.path).toBe('/api');
    // A session cookie: no expiry, so it dies with the browser.
    expect(cookie?.expires).toBe(-1);
    expect(await page.evaluate(() => document.cookie)).not.toContain('bb_admin_session');
  });

  test('the secret is never persisted in web storage', async ({ page }) => {
    await login(page);
    await page.getByRole('link', { name: 'Buckets' }).click();
    await expect(page).toHaveURL(/\/buckets$/);
    const dump = await page.evaluate(() => {
      const all: Record<string, string> = {};
      for (const store of [localStorage, sessionStorage]) {
        for (let i = 0; i < store.length; i++) {
          const k = store.key(i) ?? '';
          all[k] = store.getItem(k) ?? '';
        }
      }
      return all;
    });
    expect(Object.keys(dump)).not.toContain('bytebucket_session');
    expect(JSON.stringify(dump)).not.toContain(ADMIN_SK);
  });

  test('a legacy stored secret is purged on load', async ({ page }) => {
    await page.goto('/login');
    await page.evaluate(
      ([ak, sk]) => localStorage.setItem('bytebucket_session', JSON.stringify({ accessKey: ak, secret: sk })),
      [ADMIN_AK, ADMIN_SK],
    );
    await page.reload();
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    expect(await page.evaluate(() => localStorage.getItem('bytebucket_session'))).toBeNull();
  });

  test('a session revoked server-side sends the user back to login', async ({ page }) => {
    await login(page);
    // Revoke through the API with the same cookie jar, as a logout in another tab would.
    const res = await page.request.post('/api/logout');
    expect(res.status()).toBe(204);
    await page.getByRole('link', { name: 'Buckets' }).click();
    await expect(page).toHaveURL(/\/login$/);
  });

  test('the UI runs under its CSP without violations', async ({ page }) => {
    const violations: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error' && /Content Security Policy/i.test(msg.text())) violations.push(msg.text());
    });
    await login(page);
    for (const name of ['Buckets', 'Users', 'Logs', 'Settings', 'Overview']) {
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name }).click();
      await expect(page.getByRole('heading', { name }).first()).toBeVisible();
    }
    expect(violations).toEqual([]);
  });
});
