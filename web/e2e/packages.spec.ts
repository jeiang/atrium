import type { Page } from '@playwright/test';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const on = { apps: { on: ['packages'] } };

interface Sent {
  path: string;
  body: unknown;
  key: string | undefined;
}

function writes(page: Page): Sent[] {
  const sent: Sent[] = [];
  page.on('request', (request) => {
    if (request.method() === 'GET') return;
    const path = new URL(request.url()).pathname;
    if (!path.startsWith('/web/v1/packages') && !path.startsWith('/web/v1/effects')) return;
    const type = request.headers()['content-type'] ?? '';
    sent.push({
      path,
      body: type.includes('json') ? request.postDataJSON() : undefined,
      key: request.headers()['idempotency-key'],
    });
  });
  return sent;
}

function paths(sent: Sent[]): string[] {
  return sent.map((entry) => entry.path);
}

function toast(page: Page, text: string) {
  return page.locator('.toasts .toast', { hasText: text });
}

function card(page: Page, id: number) {
  return page.locator(`li[data-package="${id}"]`);
}

function addPanel(page: Page) {
  return page.getByRole('region', { name: 'Add a package' });
}

function dialog(page: Page) {
  return page.locator('dialog[open]');
}

async function open(page: Page): Promise<void> {
  await gotoShell(page, '/apps/packages');
  await expect(card(page, 1)).toBeVisible();
}

test.describe('Packages', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the list shows each state, the quota, and where each package came from', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    await expect(page.getByRole('heading', { level: 1, name: 'Packages' })).toBeVisible();
    await expect(page.getByText('3 of 10 Ship24 lookups left this month.')).toBeVisible();
    await expect(card(page, 1)).toContainText('Tracked with Ship24');
    await expect(card(page, 2)).toContainText('Found in mail');
    await expect(card(page, 5)).toContainText('Added by you');
    await expect(card(page, 3)).toContainText('Problem with delivery');
    await expect(card(page, 6)).toContainText('Delivered');
    await expect(card(page, 4).locator('[aria-current="step"]')).toHaveText('Out for delivery');
    await expect(page.locator('li[data-package]').first()).toHaveAttribute('data-package', '3');
    await expect(page.locator('li[data-package]').last()).toHaveAttribute('data-package', '6');
    await expect(card(page, 6).getByRole('button', { name: /^Track/ })).toHaveCount(0);
  });

  test('a long list is read page by page', async ({ page }) => {
    await useScenario(page, { ...on, packages: { list: 'many' } });
    await gotoShell(page, '/apps/packages');
    await expect(page.locator('li[data-package]')).toHaveCount(136);
  });

  test('a package is added with one keyed press', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await addPanel(page).getByLabel('Tracking number').fill('ZX9988776655');
    await addPanel(page).getByLabel('Shop').fill('Willow Hardware');
    await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
    await expect(toast(page, 'Added Willow Hardware ZX9988776655.')).toBeVisible();
    await expect(addPanel(page).getByLabel('Tracking number')).toHaveValue('');
    expect(paths(sent)).toEqual(['/web/v1/packages']);
    expect(sent[0]?.body).toEqual({ tracking_number: 'ZX9988776655', merchant: 'Willow Hardware' });
    expect(sent[0]?.key).toBeTruthy();
  });

  test('an empty number is caught before anything is sent', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
    await expect(addPanel(page).getByText('Enter the tracking number from the shipping notice.')).toBeVisible();
    await expect(addPanel(page).getByLabel('Tracking number')).toBeFocused();
    expect(sent).toEqual([]);
  });

  test.describe('refused add', () => {
    test.use({ allowStatus: [409] });

    test('a duplicate number is refused at the field', async ({ page }) => {
      await useScenario(page, on);
      await open(page);
      const sent = writes(page);
      await addPanel(page).getByLabel('Tracking number').fill('3456789012');
      await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
      await expect(addPanel(page).getByText('Package 3456789012 is already listed.')).toBeVisible();
      await expect(addPanel(page).getByLabel('Tracking number')).toHaveAttribute('aria-invalid', 'true');
      expect(paths(sent)).toEqual(['/web/v1/packages']);
    });
  });

  test('tracking names the lookup it spends and sends one request', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await card(page, 2).getByRole('button', { name: 'Track Lark and Loom 9400111202555842761234' }).click();
    await expect(dialog(page)).toContainText('This uses one Ship24 lookup. 3 of 10 are left this month.');
    await expect(dialog(page).getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog(page).getByRole('button', { name: 'Track', exact: true }).click();
    await expect(toast(page, 'Tracking Lark and Loom 9400111202555842761234.')).toBeVisible();
    await expect(page.getByText('2 of 10 Ship24 lookups left this month.')).toBeVisible();
    await expect(card(page, 2)).toContainText('Tracked with Ship24');
    expect(paths(sent)).toEqual(['/web/v1/packages/2/track']);
    expect(sent[0]?.body).toEqual({});
    expect(sent[0]?.key).toBeTruthy();
  });

  test('a spent quota disables Track with the reason', async ({ page }) => {
    await useScenario(page, { ...on, packages: { quota: 'spent' } });
    await open(page);
    await expect(page.getByText('All 10 Ship24 lookups this month are used.')).toBeVisible();
    await expect(page.locator('.notice__title', { hasText: 'No Ship24 lookups left this month' })).toBeVisible();
    await expect(card(page, 2).getByRole('button', { name: /^Track/ })).toHaveAttribute('aria-disabled', 'true');
  });

  test('without Ship24 the screen offers the connection and blocks tracking', async ({ page }) => {
    await useScenario(page, { ...on, packages: { ship24: 'missing' } });
    await open(page);
    await expect(page.locator('.notice__title', { hasText: 'Ship24 is not connected' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Connect Ship24' })).toHaveAttribute(
      'href',
      /\/settings\/connections\/service\/ship24$/,
    );
    await expect(card(page, 2).getByRole('button', { name: /^Track/ })).toHaveAttribute('aria-disabled', 'true');
    await expect(page.getByRole('button', { name: 'Check now' })).toHaveAttribute('aria-disabled', 'true');
  });

  test('stopping tracking is sent at once and can be undone', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await card(page, 1).getByRole('button', { name: 'Stop tracking Juniper Outfitters 1Z999AA10123456784' }).click();
    const done = toast(page, 'Stopped tracking Juniper Outfitters 1Z999AA10123456784.');
    await expect(done).toBeVisible();
    await expect(card(page, 1)).toContainText('Found in mail');
    await done.getByRole('button', { name: 'Undo' }).click();
    await expect(toast(page, 'Tracking Juniper Outfitters 1Z999AA10123456784 again.')).toBeVisible();
    await expect(card(page, 1)).toContainText('Tracked with Ship24');
    expect(paths(sent)).toEqual([
      '/web/v1/packages/1/untrack',
      expect.stringMatching(/^\/web\/v1\/effects\/\d+\/undo$/),
    ]);
    expect(sent.every((entry) => entry.key)).toBe(true);
  });

  test('dismissing asks first with Cancel focused and cannot be undone', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await card(page, 5).getByRole('button', { name: 'Dismiss LP00123456789CN' }).click();
    await expect(dialog(page)).toContainText('This cannot be undone.');
    await expect(dialog(page).getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog(page).getByRole('button', { name: 'Cancel' }).click();
    expect(sent).toEqual([]);
    await card(page, 5).getByRole('button', { name: 'Dismiss LP00123456789CN' }).click();
    await dialog(page).getByRole('button', { name: 'Dismiss', exact: true }).click();
    await expect(toast(page, 'Dismissed LP00123456789CN.')).toBeVisible();
    await expect(card(page, 5)).toHaveCount(0);
    expect(paths(sent)).toEqual(['/web/v1/packages/5/dismiss']);
  });

  test('Check now polls once and reports what changed', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    const sent = writes(page);
    await page.getByRole('button', { name: 'Check now' }).click();
    await expect(toast(page, 'Checked 3 packages, 1 updated.')).toBeVisible();
    expect(paths(sent)).toEqual(['/web/v1/packages/poll']);
  });

  test('an empty list says where packages come from', async ({ page }) => {
    await useScenario(page, { ...on, packages: { list: 'empty' } });
    await gotoShell(page, '/apps/packages');
    await expect(page.getByRole('heading', { name: 'No packages' })).toBeVisible();
  });

  test('the screen is not found while the app is off', async ({ page }) => {
    await gotoShell(page, '/apps/packages');
    await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
  });

  test.describe('unavailable', () => {
    test.use({ allowStatus: [503] });

    test('a busy poll says so and is not retried', async ({ page }) => {
      await useScenario(page, { ...on, packages: { busy: true } });
      await open(page);
      const sent = writes(page);
      await page.getByRole('button', { name: 'Check now' }).click();
      await expect(page.getByText('A package poll is already running. Nothing was changed.')).toBeVisible();
      await page.waitForTimeout(500);
      expect(paths(sent)).toEqual(['/web/v1/packages/poll']);
    });

    test('a failed list offers a retry', async ({ page }) => {
      await useScenario(page, { ...on, packages: { list: 'error' } });
      await gotoShell(page, '/apps/packages');
      await expect(page.getByRole('heading', { name: 'Packages did not load' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    });
  });

  test.describe('unknown outcome', () => {
    test.use({ allowStatus: [409] });

    test('an unknown untrack is said plainly and not retried', async ({ page }) => {
      await useScenario(page, { ...on, packages: { unknown: true } });
      await open(page);
      const sent = writes(page);
      await card(page, 1)
        .getByRole('button', { name: /^Stop tracking/ })
        .click();
      await expect(card(page, 1).getByText('The outcome is unknown')).toBeVisible();
      await expect(card(page, 1).getByText(/did not try again/)).toBeVisible();
      expect(paths(sent)).toEqual(['/web/v1/packages/1/untrack']);
    });

    test('a refused track keeps the dialog open with the reason', async ({ page }) => {
      await useScenario(page, {
        ...on,
        packages: { refuse: { code: 'refused', message: 'The Ship24 monthly quota of 10 lookups is used up.' } },
      });
      await open(page);
      await card(page, 2)
        .getByRole('button', { name: /^Track/ })
        .click();
      await dialog(page).getByRole('button', { name: 'Track', exact: true }).click();
      await expect(dialog(page).getByText('atrium refused this')).toBeVisible();
      await expect(
        dialog(page).getByText('The Ship24 monthly quota of 10 lookups is used up. Nothing was changed.'),
      ).toBeVisible();
    });
  });
});

test.describe('Packages while offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('every command is refused at once and nothing is sent', async ({ page }) => {
    await useScenario(page, on);
    await open(page);
    await goUnreachable(page);
    const sent = writes(page);
    await expect(page.locator('.notice__title', { hasText: 'Cannot reach atrium' })).toBeVisible();
    await expect(addPanel(page).getByRole('button', { name: 'Add', exact: true })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    await addPanel(page).getByLabel('Tracking number').fill('ZX1');
    await addPanel(page).getByLabel('Tracking number').press('Enter');
    for (const name of [/^Track Lark/, /^Stop tracking Juniper/, /^Dismiss Juniper/, /^Check now/]) {
      const button = page.getByRole('button', { name });
      await expect(button).toHaveAttribute('aria-disabled', 'true');
      await button.click({ force: true });
    }
    await expect(dialog(page)).toHaveCount(0);
    expect(sent).toEqual([]);
  });
});

test.describe('packages layouts', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({
        viewport: { width: layout.width, height: layout.height },
        hasTouch: layout.touch,
        allowStatus: [409],
      });

      test('the list with a refused number fits, rounds and passes axe', async ({ page }) => {
        await useScenario(page, on);
        await open(page);
        await addPanel(page).getByLabel('Tracking number').fill('3456789012');
        await addPanel(page).getByLabel('Tracking number').press('Enter');
        await expect(addPanel(page).getByText('Package 3456789012 is already listed.')).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });

      test('the dismiss confirmation fits and passes axe', async ({ page }) => {
        await useScenario(page, on);
        await open(page);
        await card(page, 5).getByRole('button', { name: 'Dismiss LP00123456789CN' }).click();
        await expect(dialog(page).getByRole('button', { name: 'Cancel' })).toBeFocused();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});
