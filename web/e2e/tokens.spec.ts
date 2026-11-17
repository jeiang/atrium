import type { Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks';
import { expect, goUnreachable, layouts, reconnect, test, unreachableConsole, useScenario } from './fixtures';

interface Sent {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function sent(page: Page): Promise<Sent[]> {
  return page.evaluate(
    () => (globalThis as typeof globalThis & { atriumTokenRequests?: Sent[] }).atriumTokenRequests ?? [],
  );
}

async function openTokens(page: Page): Promise<void> {
  await gotoShell(page, '/settings/sign-in');
  await expect(page.getByRole('heading', { level: 2, name: 'API tokens' })).toBeVisible();
  await expect(activeList(page).getByRole('listitem')).toHaveCount(3);
}

function activeList(page: Page) {
  return page.getByRole('list', { name: 'Active tokens' });
}

function endedList(page: Page) {
  return page.getByRole('list', { name: 'Revoked and expired tokens' });
}

function shownSecret(page: Page) {
  return page.getByRole('dialog').getByLabel('Token');
}

test.describe('API tokens', () => {
  test('Settings › Sign-in lists active and ended tokens', async ({ page }) => {
    await gotoShell(page, '/settings');
    await page
      .getByRole('navigation', { name: 'Settings pages' })
      .getByRole('link', { name: /^Sign-in/ })
      .click();
    await expect(page).toHaveURL(/\/settings\/sign-in$/);
    await expect(activeList(page).getByRole('listitem')).toHaveCount(3);
    const phone = activeList(page).getByRole('listitem').filter({ hasText: 'Shortcuts on my phone' });
    await expect(phone).toContainText('Made in atrium');
    await expect(phone).toContainText('never expires');
    await expect(activeList(page).getByRole('listitem').filter({ hasText: 'Garden sensor bridge' })).toContainText(
      'agent',
    );
    await expect(activeList(page).getByRole('listitem').filter({ hasText: 'OIDC access token' })).toContainText(
      'From the sign-in provider for sam@example.test',
    );
    const old = endedList(page).getByRole('listitem');
    await expect(old).toHaveCount(1);
    await expect(old).toContainText('Old laptop');
    await expect(old).toContainText('Revoked');
    await expect(old.getByRole('button')).toHaveCount(0);
    expect(await sent(page)).toEqual([]);
  });

  test('a new token is shown once in the dialog and is gone when it closes', async ({ page }) => {
    await openTokens(page);
    await page.getByRole('button', { name: 'New token' }).click();
    const dialog = page.getByRole('dialog', { name: 'New API token' });
    await expect(dialog.getByLabel('Name')).toBeFocused();
    await dialog.getByRole('button', { name: 'Make the token' }).click();
    await expect(dialog.getByText('Give the token a name.')).toBeVisible();
    expect(await sent(page)).toEqual([]);

    await dialog.getByLabel('Name').fill('  Kitchen tablet  ');
    await dialog.getByLabel('For an agent').check();
    await dialog.getByRole('button', { name: 'Make the token' }).click();
    const shown = page.getByRole('dialog', { name: 'Copy the token for Kitchen tablet' });
    await expect(shown).toBeVisible();
    await expect(shownSecret(page)).toHaveValue(/^atr_mock0005x+\d+$/);
    await expect(shownSecret(page)).toHaveAttribute('readonly', '');
    const writes = await sent(page);
    expect(writes.map((item) => [item.method, item.path, item.body])).toEqual([
      ['POST', '/web/v1/auth/tokens', { name: 'Kitchen tablet', agent: true }],
    ]);
    expect(writes[0]?.key).toMatch(UUID);
    await expect(activeList(page).getByRole('listitem')).toHaveCount(4);

    await shown.getByRole('button', { name: 'Done' }).click();
    await expect(shown).toBeHidden();
    await expect(page.getByText(/^atr_mock/)).toHaveCount(0);
    await page.getByRole('button', { name: 'New token' }).click();
    await expect(page.getByRole('dialog', { name: 'New API token' }).getByLabel('Name')).toHaveValue('');
    await expect(page.locator('input[readonly]')).toHaveCount(0);
  });

  test('an expiry date is sent as the end of that day', async ({ page }) => {
    await openTokens(page);
    await page.getByRole('button', { name: 'New token' }).click();
    const dialog = page.getByRole('dialog', { name: 'New API token' });
    await dialog.getByLabel('Name').fill('Holiday script');
    const min = await dialog.getByLabel('Expires on').getAttribute('min');
    expect(min).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    await dialog.getByLabel('Expires on').fill('2099-03-04');
    await dialog.getByRole('button', { name: 'Make the token' }).click();
    await expect(page.getByRole('dialog', { name: 'Copy the token for Holiday script' })).toBeVisible();
    const [write] = await sent(page);
    const body = write?.body as { name: string; expires_at: string };
    const expected = await page.evaluate(() => new Date(2099, 2, 4, 23, 59, 59).toISOString());
    expect(body).toEqual({ name: 'Holiday script', expires_at: expected });
    await page.keyboard.press('Escape');
    await expect(activeList(page).getByRole('listitem').filter({ hasText: 'Holiday script' })).toContainText('expires');
  });

  test('revoking asks first with Cancel focused and sends one keyed delete', async ({ page }) => {
    await openTokens(page);
    await page.getByRole('button', { name: 'Revoke Shortcuts on my phone' }).focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Revoke Shortcuts on my phone?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Revoke Shortcuts on my phone' })).toBeFocused();
    expect(await sent(page)).toEqual([]);

    await page.keyboard.press('Enter');
    await dialog.getByRole('button', { name: 'Revoke token' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('.toast__text', { hasText: 'Revoked Shortcuts on my phone.' })).toBeVisible();
    await expect(activeList(page).getByRole('listitem')).toHaveCount(2);
    await expect(endedList(page).getByRole('listitem')).toHaveCount(2);
    const writes = await sent(page);
    expect(writes.map((item) => [item.method, item.path])).toEqual([['DELETE', '/web/v1/auth/tokens/1']]);
    expect(writes[0]?.key).toMatch(UUID);
  });

  test('an empty list says so', async ({ page }) => {
    await useScenario(page, { tokens: { empty: true } });
    await gotoShell(page, '/settings/sign-in');
    await expect(page.getByText('No API tokens')).toBeVisible();
    await expect(page.getByRole('button', { name: 'New token' })).toBeVisible();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500, 422], allowConsole: [/500|422/] });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { tokens: { fail: true } });
      await gotoShell(page, '/settings/sign-in');
      await expect(page.getByText('The tokens did not load')).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'New token' })).toHaveCount(0);
    });

    test('a refused token keeps the dialog and its text', async ({ page }) => {
      await openTokens(page);
      await page.getByRole('button', { name: 'New token' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API token' });
      await dialog.getByLabel('Name').fill('x'.repeat(100));
      await dialog.getByLabel('Name').evaluate((input: HTMLInputElement) => {
        input.removeAttribute('maxlength');
      });
      await dialog.getByLabel('Name').fill('x'.repeat(101));
      await dialog.getByRole('button', { name: 'Make the token' }).click();
      await expect(dialog.getByText('A name of at most 100 characters is required.')).toBeVisible();
      await expect(dialog.getByLabel('Name')).toHaveValue('x'.repeat(101));
      await expect(page.locator('input[readonly]')).toHaveCount(0);
    });
  });

  test.describe('when atrium cannot be reached', () => {
    test.use({ allowConsole: unreachableConsole });

    test('making and revoking are refused at once and nothing is sent', async ({ page }) => {
      await openTokens(page);
      await page.getByRole('button', { name: 'New token' }).click();
      const make = page.getByRole('dialog', { name: 'New API token' });
      await make.getByLabel('Name').fill('Kitchen tablet');
      await goUnreachable(page);
      await make.getByRole('button', { name: 'Make the token' }).click();
      await expect(make.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(make.getByLabel('Name')).toHaveValue('Kitchen tablet');
      await make.getByRole('button', { name: 'Cancel' }).click();
      await expect(make).toBeHidden();

      await page.getByRole('button', { name: 'Revoke Shortcuts on my phone' }).click();
      const revoke = page.getByRole('dialog', { name: 'Revoke Shortcuts on my phone?' });
      await revoke.getByRole('button', { name: 'Revoke token' }).click();
      await expect(revoke.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(revoke).toBeVisible();
      expect(await sent(page)).toEqual([]);
    });

    test('a token whose answer is lost says the outcome is unknown and is not sent again', async ({ page }) => {
      await useScenario(page, { tokens: { lose: true } });
      await openTokens(page);
      await page.getByRole('button', { name: 'New token' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API token' });
      await dialog.getByLabel('Name').fill('Kitchen tablet');
      await dialog.getByRole('button', { name: 'Make the token' }).click();
      await expect(dialog.getByText(/^The outcome is unknown\..*atrium did not try again\.$/)).toBeVisible();
      await expect(page.locator('input[readonly]')).toHaveCount(0);
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/auth/tokens']);
      await reconnect(page, /\/web\/v1\/auth\/tokens$/);
      await expect(activeList(page).getByRole('listitem').filter({ hasText: 'Kitchen tablet' })).toBeVisible();
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/auth/tokens']);
    });

    test('a revoke whose answer is lost is not sent again', async ({ page }) => {
      await useScenario(page, { tokens: { lose: true } });
      await openTokens(page);
      await page.getByRole('button', { name: 'Revoke Garden sensor bridge' }).click();
      const dialog = page.getByRole('dialog', { name: 'Revoke Garden sensor bridge?' });
      await dialog.getByRole('button', { name: 'Revoke token' }).click();
      await expect(dialog.getByText(/^The outcome is unknown\./)).toBeVisible();
      await reconnect(page, /\/web\/v1\/auth\/tokens$/);
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/auth/tokens/2']);
    });

    test('an unopened list says so', async ({ page }) => {
      await gotoShell(page, '/settings/general');
      await expect(page.getByRole('heading', { level: 2, name: 'General' })).toBeVisible();
      await goUnreachable(page);
      await page
        .getByRole('navigation', { name: 'Settings pages' })
        .getByRole('link', { name: /^Sign-in/ })
        .click();
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' }).last()).toBeVisible();
      await expect(page.getByText('The tokens load when atrium answers again.')).toBeVisible();
      await expect(page.getByRole('button', { name: 'New token' })).toHaveCount(0);
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      const check = async (scope = 'body') => {
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
        if (layout.touch) await expectTouchTargets(page, scope);
        await expectNoBlue(page);
      };
      await openTokens(page);
      await check();
      await expectFocusVisible(page, 'main');

      await page.getByRole('button', { name: 'New token' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API token' });
      await expect(dialog).toBeVisible();
      await check('dialog[open]');
      await dialog.getByLabel('Name').fill('Kitchen tablet');
      await dialog.getByRole('button', { name: 'Make the token' }).click();
      await expect(page.getByRole('dialog', { name: 'Copy the token for Kitchen tablet' })).toBeVisible();
      await check('dialog[open]');
      await page.getByRole('dialog').getByRole('button', { name: 'Done' }).click();
      await expect(page.getByRole('dialog')).toHaveCount(0);

      await page.getByRole('button', { name: 'Revoke Shortcuts on my phone' }).click();
      await expect(page.getByRole('dialog', { name: 'Revoke Shortcuts on my phone?' })).toBeVisible();
      await check('dialog[open]');
      await page.keyboard.press('Escape');
    });

    test(`design checks for an empty list at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await useScenario(page, { tokens: { empty: true } });
      await gotoShell(page, '/settings/sign-in');
      await expect(page.getByText('No API tokens')).toBeVisible();
      await expectNoSeriousAxeFindings(page);
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, ['.rail', '.tabbar']);
      if (layout.touch) await expectTouchTargets(page);
      await expectNoBlue(page);
    });
  }
});
