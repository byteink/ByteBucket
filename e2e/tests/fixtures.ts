import { expect, type Page } from '@playwright/test';

// Admin credentials default to the committed local-compose superuser so the
// suite runs with no setup; override via env for a different deployment.
export const ADMIN_AK = process.env.BB_ADMIN_AK ?? 'APE6at7CMFvJaEJjnmbC';
export const ADMIN_SK = process.env.BB_ADMIN_SK ?? '40ylGQ3lRaxE/SQFRZrHZY+e+XD7CBMVa8ioUsAO';

// adminHeaders authenticate direct API calls (page.request) used to seed state
// that the UI then displays, without going through the browser forms.
export const adminHeaders = {
  'X-Admin-AccessKey': ADMIN_AK,
  'X-Admin-Secret': ADMIN_SK,
};

// login drives the real login form and asserts the landing on the dashboard.
// Used by every authenticated spec; the auth spec exercises the form directly.
export async function login(page: Page, ak = ADMIN_AK, sk = ADMIN_SK): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Access key').fill(ak);
  await page.getByLabel('Secret').fill(sk);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/dashboard$/);
}
