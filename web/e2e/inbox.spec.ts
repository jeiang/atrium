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
import {
  expect,
  goReachable,
  goUnreachable,
  layouts,
  setUnreachable,
  storedKeys,
  test,
  unreachableConsole,
  useScenario,
} from './fixtures.ts';

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
    await expect(send).toHaveAttribute('aria-disabled', 'true');
    await expect(draft.locator('.approval__row')).not.toHaveAttribute('inert');
    await send.press('Enter');
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
    test.use({ allowStatus: [404, 409, 500], allowConsole: unreachableConsole });

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
    test('an unreachable atrium says so and decides nothing', async ({ page }) => {
      await gotoShell(page, '/inbox');
      await expect(card(page, 'draft:7')).toBeVisible();
      await setUnreachable(page, true);
      await page
        .getByRole('table', { name: 'Needs you' })
        .getByRole('row', { name: /Move the dentist/ })
        .click();
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' })).toBeVisible();
      await setUnreachable(page, false);
      await expect(card(page, 'proposal:12')).toContainText('Move the dentist visit to Friday', { timeout: 15_000 });
      expect(await requests(page)).toEqual([]);
    });

    test('a decision while atrium is unreachable is refused at once and never sent later', async ({ page }) => {
      const posts: string[] = [];
      page.on('request', (request) => {
        if (request.method() === 'POST') posts.push(request.url());
      });
      await gotoShell(page, '/inbox/proposal:14');
      const item = card(page, 'proposal:14');
      await expect(item.getByRole('button', { name: 'Approve' })).toBeVisible();
      await goUnreachable(page);
      await item.getByRole('button', { name: 'Approve' }).click();
      await expect(page.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(item.getByRole('button', { name: 'Approve' })).toBeVisible();
      await goReachable(page);
      await page.waitForTimeout(1_000);
      expect(posts).toEqual([]);
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

  test('the Inbox list and its items are kept on the device', async ({ page }) => {
    await gotoShell(page, '/inbox/proposal:14');
    await expect(card(page, 'proposal:14').getByRole('button', { name: 'Approve' })).toBeVisible();
    await expect
      .poll(() => storedKeys(page))
      .toEqual(expect.arrayContaining(['["inbox","queue"]', '["inbox","item","proposal:14"]']));
    expect((await storedKeys(page)).filter((key) => key.startsWith('["session'))).toEqual([]);
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

test.describe('every queue kind in the inbox', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [404] });

  test('the table lists asks, proposals, drafts and reminders with the queue counts and the badge agrees', async ({
    page,
  }) => {
    await useScenario(page, { inbox: { mix: true } });
    await gotoShell(page, '/inbox');
    await expect(page.getByText('7 items waiting on you')).toBeVisible();
    await expect(page.getByText('1 ask, 2 proposals, 1 draft, 3 reminders')).toBeVisible();
    await waiting(page, 7);
    const table = page.getByRole('table', { name: 'Needs you' });
    await expect(table.getByRole('row')).toHaveCount(8);
    await expect(table.getByRole('row').nth(1)).toContainText('Which room should the garden meeting use?');
    await expect(table.getByRole('row', { name: /Renew the library card/ })).toContainText('Due');
  });

  test('an off app hides its items, its counts and the badge drops with them', async ({ page }) => {
    await useScenario(page, { inbox: { mix: true }, modules: { calendar: 'off', assistant: 'off' } });
    await gotoShell(page, '/inbox');
    await expect(page.getByText('3 items waiting on you')).toBeVisible();
    await waiting(page, 3);
    await expect(page.getByText(/\bask\b/)).toHaveCount(0);
    const table = page.getByRole('table', { name: 'Needs you' });
    await expect(table.getByRole('row', { name: /Which room/ })).toHaveCount(0);
    await expect(table.getByRole('row', { name: /Seed swap/ })).toHaveCount(0);
    await expect(table.getByRole('row', { name: /Move the dentist/ })).toHaveCount(0);
  });

  test('a due task is marked done from the inbox, then undone', async ({ page }) => {
    await useScenario(page, { inbox: { mix: true } });
    await gotoShell(page, '/inbox/task:31');
    const item = page.locator('[data-item="task:31"]');
    await expect(item).toContainText('Renew the library card');
    await expect(item).toContainText('Chat: Weekend errands');
    await expect(item.getByRole('button', { name: 'Snooze' })).toHaveCount(0);
    await item.getByRole('button', { name: 'Done' }).click();
    await expect(item).toContainText('Marked done');
    await waiting(page, 6);
    await item.getByRole('button', { name: 'Undo' }).click();
    await expect(item.getByRole('button', { name: 'Done' })).toBeVisible();
    await waiting(page, 7);
    const sent = await requests(page);
    expect(sent.map((r) => `${r.method} ${r.path}`)).toEqual([
      'POST /web/v1/commitments/task:31/done',
      'POST /web/v1/undo/write:900',
    ]);
    expect(new Set(sent.map((r) => r.key)).size).toBe(2);
    for (const r of sent) expect(r.key).toMatch(uuid);
  });

  test('a question takes an answer once', async ({ page }) => {
    await useScenario(page, { inbox: { mix: true } });
    await gotoShell(page, '/inbox/question:42');
    const item = page.locator('[data-item="question:42"]');
    await item.getByRole('textbox', { name: 'Your answer' }).fill('The library room, please.');
    await item.getByRole('button', { name: 'Send answer' }).click();
    await expect(item).toContainText('Answer sent');
    await waiting(page, 6);
    expect((await requests(page)).map((r) => r.path)).toEqual(['/web/v1/threads/42/answer']);
  });

  test.describe('refusals', () => {
    test.use({ allowStatus: [404, 409] });

    test('a question answered elsewhere says so', async ({ page }) => {
      await useScenario(page, { inbox: { mix: true, decide: 'conflict' } });
      await gotoShell(page, '/inbox/question:42');
      const item = page.locator('[data-item="question:42"]');
      await item.getByRole('textbox', { name: 'Your answer' }).fill('The hall.');
      await item.getByRole('button', { name: 'Send answer' }).click();
      await expect(item).toContainText('Already answered');
    });

    test('a reminder done elsewhere says it was handled', async ({ page }) => {
      await useScenario(page, { inbox: { mix: true } });
      await gotoShell(page, '/inbox/commitment:8');
      const item = page.locator('[data-item="commitment:8"]');
      await expect(item.getByRole('button', { name: 'Done' })).toBeVisible();
      await streamOpen(page);
      await page.evaluate(() => {
        const channel = new BroadcastChannel('atrium-mock-events');
        channel.postMessage({ topic: 'tasks', decide: 'commitment:8' });
        channel.postMessage({ topic: 'attention' });
        channel.close();
      });
      await expect(page.getByRole('heading', { name: 'Handled elsewhere' })).toBeVisible();
    });
  });

  test('an item with no operation shows its details and no invented action', async ({ page }) => {
    await useScenario(page, { inbox: { mix: true } });
    await gotoShell(page, '/inbox/event_candidate:9001');
    const item = page.locator('[data-item="event_candidate:9001"]');
    await expect(item).toContainText('Seed swap on Saturday at ten');
    await expect(item.getByRole('button')).toHaveCount(0);
  });

  for (const layout of layouts) {
    test(`the mixed inbox passes the design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await useScenario(page, { inbox: { mix: true } });
      await gotoShell(page, '/inbox/task:31');
      await expect(page.locator('[data-item="task:31"]')).toContainText('Renew the library card');
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, edges);
      if (layout.touch) await expectTouchTargets(page);
      await expectNoBlue(page);
      await expectNoSeriousAxeFindings(page);
    });
  }
});

test.describe('an unknown outcome in the inbox', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  async function choose(page: Page, item: Locator, label: string, ask: string): Promise<Locator> {
    await item.getByRole('button', { name: label }).click();
    const dialog = page.getByRole('dialog', { name: ask });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    return dialog;
  }

  test('a stuck change is marked as done once, after a confirm that starts on Cancel', async ({ page }) => {
    await useScenario(page, { inbox: { stuck: true } });
    await gotoShell(page, '/inbox/proposal:15');
    const item = card(page, 'proposal:15');
    await expect(item).toContainText('it is unknown whether it happened');
    await expect(item.getByRole('button', { name: 'Approve' })).toHaveCount(0);
    await expect(item.getByRole('button', { name: 'Decline' })).toHaveCount(0);
    await expect(item.getByRole('button', { name: 'Mark as not done' })).toBeVisible();

    let dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    expect(await requests(page)).toEqual([]);

    dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
    await expect(dialog).toContainText('atrium records that the change was made and does not run it again.');
    await dialog.getByRole('button', { name: 'Mark as done' }).click();
    await expect(item).toContainText(/Marked as done at \d\d:\d\d/);
    await expect(item.getByRole('button', { name: 'Mark as done' })).toHaveCount(0);
    const sent = await requests(page);
    expect(sent).toEqual([{ method: 'POST', path: '/web/v1/approvals/15/resolve', key: expect.stringMatching(uuid) }]);
  });

  test('a stuck undo offers the undo outcomes and records the one chosen', async ({ page }) => {
    await useScenario(page, { inbox: { stuck: true } });
    await gotoShell(page, '/inbox/proposal:16');
    const item = card(page, 'proposal:16');
    await expect(item).toContainText('it is unknown whether the change was reversed');
    await expect(item.getByRole('button', { name: 'Mark as done' })).toHaveCount(0);
    const dialog = await choose(page, item, 'Mark undo as not done', 'Mark the undo as not done?');
    await expect(dialog).toContainText('atrium records that the change is still in place.');
    await dialog.getByRole('button', { name: 'Mark undo as not done' }).click();
    await expect(item).toContainText(/Undo marked as not done at \d\d:\d\d/);
    expect(await requests(page)).toEqual([
      { method: 'POST', path: '/web/v1/approvals/16/resolve', key: expect.stringMatching(uuid) },
    ]);
  });

  test('an email with an unknown send is marked as sent and never sent again', async ({ page }) => {
    await useScenario(page, { inbox: { stuck: true } });
    await gotoShell(page, '/inbox/draft:8');
    const item = card(page, 'draft:8');
    await expect(item).toContainText('The Sent folder can be empty even when the email was delivered.');
    await expect(item.getByRole('button', { name: /^Send email/ })).toHaveCount(0);
    const dialog = await choose(page, item, 'Mark as sent', 'Mark this email as sent?');
    await expect(dialog).toContainText('atrium records it as sent and does not send it again.');
    await dialog.getByRole('button', { name: 'Mark as sent' }).click();
    await expect(item).toContainText(/Sent at \d\d:\d\d/);
    expect(await requests(page)).toEqual([
      { method: 'POST', path: '/web/v1/drafts/8/resolve', key: expect.stringMatching(uuid) },
    ]);
  });

  test('an email returned to drafts can be sent again', async ({ page }) => {
    await useScenario(page, { inbox: { stuck: true } });
    await gotoShell(page, '/inbox/draft:8');
    const item = card(page, 'draft:8');
    const dialog = await choose(page, item, 'Return to drafts', 'Return this email to drafts?');
    await expect(dialog).toContainText('the recipient gets it twice');
    await dialog.getByRole('button', { name: 'Return to drafts' }).click();
    await expect(item.getByRole('button', { name: 'Send email to Joris' })).toBeVisible();
    await expect(item.getByRole('button', { name: 'Return to drafts' })).toHaveCount(0);
    expect(await requests(page)).toEqual([
      { method: 'POST', path: '/web/v1/drafts/8/resolve', key: expect.stringMatching(uuid) },
    ]);
  });

  test('the stuck items wait in the list as Stuck', async ({ page }) => {
    await useScenario(page, { inbox: { stuck: true } });
    await gotoShell(page, '/inbox');
    const table = page.getByRole('table', { name: 'Needs you' });
    await expect(table.getByRole('row', { name: /Start watering the vegetable beds/ })).toContainText('Stuck');
    await expect(table.getByRole('row', { name: /Allotment key handover/ })).toContainText('Send outcome unknown');
  });

  test.describe('with a refusal or a lost connection', () => {
    test.use({ allowStatus: [409, 422], allowConsole: unreachableConsole });

    test('marking while atrium is unreachable is refused at once and never sent later', async ({ page }) => {
      await useScenario(page, { inbox: { stuck: true } });
      await gotoShell(page, '/inbox/proposal:15');
      const item = card(page, 'proposal:15');
      await expect(item.getByRole('button', { name: 'Mark as done' })).toBeVisible();
      await goUnreachable(page);
      let dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
      await dialog.getByRole('button', { name: 'Mark as done' }).click();
      await expect(page.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(dialog).toBeHidden();
      await expect(item.getByRole('button', { name: 'Mark as done' })).toBeVisible();
      expect(await requests(page)).toEqual([]);
      await goReachable(page);
      dialog = await choose(page, item, 'Mark as not done', 'Mark this change as not done?');
      await dialog.getByRole('button', { name: 'Mark as not done' }).click();
      await expect(item).toContainText(/Marked as not done at \d\d:\d\d/);
      const sent = await requests(page);
      expect(sent).toEqual([
        { method: 'POST', path: '/web/v1/approvals/15/resolve', key: expect.stringMatching(uuid) },
      ]);
    });

    test('a network failure says the outcome is unknown and records nothing again', async ({ page }) => {
      await useScenario(page, { inbox: { stuck: true, decide: 'network' } });
      await gotoShell(page, '/inbox/draft:8');
      const item = card(page, 'draft:8');
      const dialog = await choose(page, item, 'Mark as sent', 'Mark this email as sent?');
      await dialog.getByRole('button', { name: 'Mark as sent' }).click();
      await expect(page.getByText('The outcome is unknown')).toBeVisible();
      await expect(item).toContainText(/Sent at \d\d:\d\d/);
      expect(await requests(page)).toHaveLength(1);
    });

    test('a refusal says what atrium answered and records nothing', async ({ page }) => {
      await useScenario(page, { inbox: { stuck: true, decide: 'conflict' } });
      await gotoShell(page, '/inbox/proposal:15');
      const item = card(page, 'proposal:15');
      const dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
      const reload = page.waitForResponse((response) => response.url().endsWith('/web/v1/proposals/15'));
      await dialog.getByRole('button', { name: 'Mark as done' }).click();
      await expect(page.getByText('atrium refused this')).toBeVisible();
      await expect(page.getByText('Nothing was recorded.', { exact: false })).toBeVisible();
      await reload;
      await expect(item.getByRole('button', { name: 'Mark as done' })).toBeVisible();
      expect(await requests(page)).toHaveLength(1);
    });
  });

  for (const layout of layouts) {
    test(`the stuck change, its confirm and the unknown send pass the design checks at ${layout.name}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await useScenario(page, { inbox: { stuck: true } });
      const checks = async (): Promise<void> => {
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await expectNoSeriousAxeFindings(page);
      };
      await gotoShell(page, '/inbox/proposal:15');
      const item = card(page, 'proposal:15');
      await expect(item.getByRole('button', { name: 'Mark as done' })).toBeVisible();
      await checks();
      const dialog = await choose(page, item, 'Mark as done', 'Mark this change as done?');
      await checks();
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await gotoShell(page, '/inbox/draft:8');
      await expect(card(page, 'draft:8').getByRole('button', { name: 'Mark as sent' })).toBeVisible();
      await checks();
    });
  }
});
