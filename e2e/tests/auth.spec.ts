import { test, expect } from '@playwright/test';
import { login, ADMIN_AK } from './fixtures';

test.describe('authentication', () => {
  test('unauthenticated visit redirects to login', async ({ page }) => {
    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/login$/);
    await expect(page.getByRole('heading', { name: 'ByteBucket Admin' })).toBeVisible();
  });

  test('bad credentials show an error and stay on login', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Access key').fill('wrong');
    await page.getByLabel('Secret').fill('wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByText('Invalid admin credentials')).toBeVisible();
    await expect(page).toHaveURL(/\/login$/);
  });

  test('valid credentials land on the dashboard', async ({ page }) => {
    await login(page);
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
    // The header shows the authenticated access key.
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
});
