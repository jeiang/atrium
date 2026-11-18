import { expect, type Page, test } from '@playwright/test';

const live = process.env.ATRIUM_LIVE_URL ?? '';
const elsewhere = process.env.ATRIUM_LIVE_ELSEWHERE ?? '';
const target = '/third-party-notices.txt?from=mail';

function documents(page: Page): string[] {
  const seen: string[] = [];
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) seen.push(frame.url());
  });
  return seen;
}

async function expectSignedInAt(page: Page): Promise<void> {
  await expect(page).toHaveURL(live + target);
  await expect(page.locator('body')).toContainText('================');
  await expect(page.getByText('Continue to Atrium')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Sign in/ })).toHaveCount(0);
}

async function signIn(page: Page): Promise<void> {
  await page.goto(live + target);
  await expect(page).toHaveURL(`${live}/login?next=${encodeURIComponent(target)}`);
  await page.getByRole('button', { name: /^Sign in/ }).click();
  await expectSignedInAt(page);
}

test.beforeAll(() => {
  expect(live, 'ATRIUM_LIVE_URL comes from TestGuardBrowserSignInReturnsAcrossSites').not.toBe('');
  expect(elsewhere, 'ATRIUM_LIVE_ELSEWHERE comes from TestGuardBrowserSignInReturnsAcrossSites').not.toBe('');
});

test('sign-in returns to the page the operator asked for with a Strict session cookie', async ({ page, context }) => {
  const seen = documents(page);
  await signIn(page);
  expect(seen.filter((url) => new URL(url).pathname === '/login')).toHaveLength(1);
  const cookies = await context.cookies(live);
  expect(cookies.find((cookie) => cookie.name === 'atrium_session')?.sameSite).toBe('Strict');
  expect(cookies.find((cookie) => cookie.name === 'atrium_csrf')?.sameSite).toBe('Strict');
  expect(cookies.find((cookie) => cookie.name === 'atrium_oidc_state')).toBeUndefined();
});

test('a link opened from another site lands signed in after one same-site continue', async ({ page }) => {
  await signIn(page);
  await page.goto(elsewhere);
  const seen = documents(page);
  await page.getByRole('link', { name: 'Open in Atrium' }).click();
  await expectSignedInAt(page);
  expect(seen).toEqual([live + target, live + target]);
});
