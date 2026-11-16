import type { Locator, Page } from '@playwright/test';
import type { InboxRequest } from '../src/mocks/inbox.ts';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, layouts, test, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet'];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

function requests(page: Page): Promise<InboxRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumInboxRequests') ?? []);
}

function card(page: Page, key: string): Locator {
  return page.locator(`[data-approval="${key}"]`);
}

function address(key: string): RegExp {
  const [type, id] = key.split(':');
  return new RegExp(`/inbox/${type}(:|%3A)${id}$`);
}

async function press(page: Page, target: Locator, ms: number): Promise<void> {
  const box = await target.boundingBox();
  if (!box) throw new Error('the hold button has no box');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(ms);
  await page.mouse.up();
}

async function streamOpen(page: Page): Promise<void> {
  await expect.poll(() => page.evaluate(() => Reflect.get(globalThis, 'atriumMockStreams') ?? 0)).toBeGreaterThan(0);
}

async function waiting(page: Page, count: number): Promise<void> {
  const nav = page.getByRole('navigation', { name: 'Main' });
  const name = count === 0 ? /^Inbox$/ : new RegExp(`^Inbox\\W+${count} waiting$`);
  await expect(nav.getByRole('link', { name })).toBeVisible();
}

test.describe('inbox on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the list opens the selected card', async ({ page }) => {
    await gotoShell(page, '/inbox');
    await expect(page.getByRole('heading', { level: 1, name: 'Inbox' })).toBeVisible();
    await expect(page.getByText('3 items waiting on you')).toBeVisible();
    const table = page.getByRole('table', { name: 'Needs you' });
    await expect(table.getByRole('row')).toHaveCount(4);
    await expect(card(page, 'draft:7')).toContainText('Shared garden plans');

    await table.getByRole('row', { name: /Reset the garden weather station/ }).click();
    await expect(page).toHaveURL(address('proposal:14'));
    const unknown = card(page, 'proposal:14');
    await expect(unknown).toContainText('Reset station');
    await expect(unknown).toContainText('Garden shed');
    await expect(unknown).toContainText('The station restarts and keeps its readings.');
  });

  const addresses = [
    { path: '/inbox/proposal:12', key: 'proposal:12', text: 'Move the dentist visit to Friday' },
    { path: '/inbox/proposal%3A12', key: 'proposal:12', text: 'Move the dentist visit to Friday' },
    { path: '/proposals/12', key: 'proposal:12', text: 'Move the dentist visit to Friday' },
    { path: '/drafts/7', key: 'draft:7', text: 'Shared garden plans' },
  ];
  for (const item of addresses) {
    test(`${item.path} resolves to the ${item.key} card`, async ({ page }) => {
      await gotoShell(page, item.path);
      await expect(page).toHaveURL(address(item.key));
      await expect(card(page, item.key)).toContainText(item.text);
    });
  }

  test('a send counts down 10 seconds with Undo, then sends once and updates the badge', async ({ page }) => {
    test.setTimeout(60_000);
    await gotoShell(page, '/inbox/draft:7');
    await waiting(page, 3);
    const draft = card(page, 'draft:7');
    const send = draft.getByRole('button', { name: 'Send email to Ilse' });
    await expect(send).not.toHaveAttribute('aria-disabled');
    await send.click();
    const pending = draft.locator('.receipt[data-tone="pending"]');
    await expect(pending).toContainText(/Sending in (10|9) s/);
    await pending.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(pending).toHaveCount(0);
    expect(await requests(page)).toEqual([]);

    await expect(draft.locator('.approval__row')).not.toHaveAttribute('inert');
    await draft.getByRole('button', { name: 'Send email to Ilse' }).click();
    await page.waitForTimeout(6000);
    expect(await requests(page)).toEqual([]);
    await expect(draft.locator('.receipt')).toContainText('Sent to Ilse de Wit', { timeout: 10_000 });
    const sent = await requests(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.path).toBe('/web/v1/drafts/7/send');
    expect(sent[0]?.key).toMatch(uuid);
    await waiting(page, 2);
    await expect(page.getByRole('table', { name: 'Needs you' }).getByRole('row')).toHaveCount(3);
  });

  test('a receipt Undo reverses an approval through its effects', async ({ page }) => {
    await gotoShell(page, '/inbox/proposal:14');
    const unknown = card(page, 'proposal:14');
    await unknown.getByRole('button', { name: 'Approve' }).click();
    const receipt = unknown.locator('.receipt');
    await expect(receipt).toContainText('Approved. atrium applied it');
    await waiting(page, 2);
    await receipt.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(unknown).toContainText(/Approved, then undone on this device at \d\d:\d\d/);
    const sent = await requests(page);
    expect(sent.map((item) => item.path)).toEqual(['/web/v1/approvals/14/approve', '/web/v1/effects/500/undo']);
    expect(new Set(sent.map((item) => item.key)).size).toBe(2);
  });

  test('a decline offers no Undo the server cannot do', async ({ page }) => {
    await gotoShell(page, '/inbox/proposal:12');
    const move = card(page, 'proposal:12');
    await move.getByRole('button', { name: 'Decline' }).click();
    await expect(move.locator('.receipt')).toContainText('Declined');
    await expect(move.getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0);
    expect((await requests(page)).map((item) => item.path)).toEqual(['/web/v1/approvals/12/deny']);
  });

  test('auto-repeat from a held key decides once', async ({ page }) => {
    await gotoShell(page, '/inbox/proposal:14');
    const approve = card(page, 'proposal:14').getByRole('button', { name: 'Approve' });
    await expect(card(page, 'proposal:14').locator('.approval__row')).not.toHaveAttribute('inert');
    await approve.focus();
    await page.keyboard.down('Enter');
    await page.keyboard.down('Enter');
    await page.keyboard.down('Enter');
    await page.keyboard.up('Enter');
    await expect(card(page, 'proposal:14').locator('.receipt')).toContainText('Approved');
    await page.waitForTimeout(500);
    expect(await requests(page)).toHaveLength(1);
  });

  test.describe('with a refusal or a lost connection', () => {
    test.use({
      allowStatus: [404, 409, 500],
      allowConsole: [
        /net::ERR_FAILED|ERR_INTERNET_DISCONNECTED|served by service worker is an error|passed an Error Response to FetchEvent/,
      ],
    });

    test('a conflict reloads the item and says so', async ({ page }) => {
      await useScenario(page, { inbox: { decide: 'conflict' } });
      await gotoShell(page, '/inbox/proposal:14');
      const unknown = card(page, 'proposal:14');
      const reload = page.waitForResponse((response) => response.url().endsWith('/web/v1/proposals/14'));
      await unknown.getByRole('button', { name: 'Approve' }).click();
      await expect(page.getByText('This changed since it loaded')).toBeVisible();
      await reload;
      await expect(unknown.getByRole('button', { name: 'Approve' })).toBeVisible();
      expect(await requests(page)).toHaveLength(1);
    });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { inbox: { fail: true } });
      await gotoShell(page, '/inbox');
      await expect(page.getByRole('heading', { name: 'The Inbox did not load' })).toBeVisible({ timeout: 15_000 });
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    });
    test('offline says so and decides nothing', async ({ page, context }) => {
      await gotoShell(page, '/inbox');
      await expect(card(page, 'draft:7')).toBeVisible();
      await context.setOffline(true);
      await page
        .getByRole('table', { name: 'Needs you' })
        .getByRole('row', { name: /Move the dentist/ })
        .click();
      await expect(page.getByRole('heading', { name: 'You are offline' })).toBeVisible();
      await context.setOffline(false);
      await expect(card(page, 'proposal:12')).toContainText('Move the dentist visit to Friday');
      expect(await requests(page)).toEqual([]);
    });

    for (const path of ['/inbox/proposal:99', '/inbox/nonsense', '/proposals/abc']) {
      test(`${path} says nothing waits there`, async ({ page }) => {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { name: 'Nothing waits here' })).toBeVisible();
        await expect(page.getByRole('link', { name: 'Back to the Inbox' })).toBeVisible();
      });
    }

    test('a network failure shows an unknown outcome and sends nothing again', async ({ page }) => {
      await useScenario(page, { inbox: { decide: 'network' } });
      await gotoShell(page, '/inbox/proposal:14');
      const unknown = card(page, 'proposal:14');
      await unknown.getByRole('button', { name: 'Approve' }).click();
      await expect(page.getByText('The outcome is unknown')).toBeVisible();
      await expect(unknown).toContainText(/Approved and applied at \d\d:\d\d/);
      await page.waitForTimeout(1500);
      expect(await requests(page)).toHaveLength(1);
    });
  });

  test('a decision made elsewhere shows on the open card', async ({ page }) => {
    await gotoShell(page, '/inbox/proposal:12');
    await streamOpen(page);
    await expect(card(page, 'proposal:12').getByRole('button', { name: 'Decline' })).toBeEnabled();
    await page.evaluate(() => {
      const channel = new BroadcastChannel('atrium-mock-events');
      channel.postMessage({ topic: 'review', decide: 'proposal:12' });
      channel.close();
    });
    const move = card(page, 'proposal:12');
    await expect(move).toContainText(/Approved and applied at \d\d:\d\d/);
    await expect(move.getByRole('button', { name: 'Decline' })).toBeDisabled();
    await waiting(page, 2);
    expect(await requests(page)).toEqual([]);
  });

  test('an empty inbox says all clear', async ({ page }) => {
    await useScenario(page, { inbox: { empty: true } });
    await gotoShell(page, '/inbox');
    await expect(page.getByRole('heading', { name: 'All clear' }).first()).toBeVisible();
    await expect(page.getByText('Nothing waits on you.').first()).toBeVisible();
    await waiting(page, 0);
  });
});

test.describe('the Decide deck on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('one card at a time, sent by a hold that waits for the end, then the next card arms', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await gotoShell(page, '/');
    await page.getByRole('link', { name: /^Decide/ }).click();
    await expect(page).toHaveURL(/\/inbox$/);
    const draft = card(page, 'draft:7');
    await expect(page.locator('[data-approval]')).toHaveCount(1);
    const hold = draft.getByRole('button', { name: 'Hold to send email to Ilse' });
    await expect(hold).toHaveAttribute('aria-disabled', 'true');
    await expect(draft.locator('.approval__row')).not.toHaveAttribute('inert');

    await press(page, hold, 1200);
    await expect(hold).not.toHaveAttribute('aria-disabled');
    expect(await requests(page)).toEqual([]);

    await press(page, hold, 900);
    await expect(draft.locator('.receipt')).toContainText('Sent to Ilse de Wit');
    const next = card(page, 'proposal:12');
    await expect(next.locator('.approval__row')).toHaveAttribute('inert');
    await expect(next.locator('.approval__row')).not.toHaveAttribute('inert');
    const sent = await requests(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.path).toBe('/web/v1/drafts/7/send');
    expect(sent[0]?.key).toMatch(uuid);
    await expect(page.locator('[data-approval]')).toHaveCount(2);
  });
});

test.describe('inbox design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      test('fits, rounds, stays off blue and passes axe', async ({ page }) => {
        await gotoShell(page, '/inbox');
        await expect(card(page, 'draft:7')).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await expectNoSeriousAxeFindings(page);
      });

      test('the empty state passes axe', async ({ page }) => {
        await useScenario(page, { inbox: { empty: true } });
        await gotoShell(page, '/inbox');
        await expect(page.getByRole('heading', { name: 'All clear' }).first()).toBeVisible();
        await expectRoundedSurfaces(page, edges);
        await expectNoSeriousAxeFindings(page);
      });
    });
  }

  test('every stop shows the focus ring', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/inbox');
    await expect(card(page, 'draft:7').locator('.approval__row')).not.toHaveAttribute('inert');
    expect(await expectFocusVisible(page, 'main')).toBeGreaterThanOrEqual(3);
  });
});
