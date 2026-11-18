import type { Locator, Page } from '@playwright/test';
import type { ChatScenario } from '../src/mocks/chats.ts';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import {
  expect,
  galleryLayouts,
  goReachable,
  goUnreachable,
  layouts,
  reconnect,
  setUnreachable,
  storedKeys,
  test,
  unreachableConsole,
  useScenario,
} from './fixtures.ts';
import { expectLostAnswer } from './lost.ts';

const edgeSurfaces = ['.rail', '.tabbar'];
const desktop = layouts[2] ?? { name: 'desktop', width: 1440, height: 900, touch: false };
const finalText = 'The order needs twelve cedar boards and one box of brass screws.';

async function openChat(page: Page, path: string, chat: ChatScenario = {}): Promise<void> {
  await useScenario(page, { chat: { speed: 8, ...chat } });
  await gotoShell(page, path);
}

function transcript(page: Page) {
  return page.getByRole('list', { name: 'Messages' });
}

function composer(page: Page) {
  return page.getByRole('textbox', { name: /^(Reply|New chat message)$/ });
}

async function settled(page: Page): Promise<void> {
  await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0, { timeout: 20_000 });
  await expect(page.locator('.transcript .status-slot')).toHaveCount(0);
}

function scrollState(page: Page) {
  return page.evaluate(() => {
    const root = document.documentElement;
    return { y: window.scrollY, gap: root.scrollHeight - (window.scrollY + window.innerHeight) };
  });
}

async function showRow(card: Locator): Promise<void> {
  await expect(card).toBeVisible();
  await card
    .page()
    .evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }));
}

test.describe('chat list', () => {
  test('lists chats with their run state, pages and opens one', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat');
    await expect(page.getByRole('heading', { level: 1, name: 'Chats' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Chats' });
    await expect(list.getByRole('listitem').filter({ hasText: 'Plan the garden shed order' })).toContainText('Running');
    await expect(list.getByRole('listitem').filter({ hasText: 'Send the book club reminder' })).toContainText(
      'Waiting for you',
    );
    await expect(list.getByRole('listitem')).toHaveCount(30);
    await page.getByRole('button', { name: 'Show more chats' }).click();
    await expect(list.getByRole('listitem')).toHaveCount(43);
    await expect(page.getByRole('button', { name: 'Show more chats' })).toHaveCount(0);
    await list.getByRole('link', { name: /Winter tyre sizes/ }).click();
    await expect(page).toHaveURL(/\/chat\/9$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Winter tyre sizes' })).toBeFocused();
  });

  test('shows the empty state with the action that starts a chat', async ({ page }) => {
    await openChat(page, '/chat', { empty: true });
    await expect(page.getByRole('heading', { name: 'No chats yet' })).toBeVisible();
    await page.getByRole('link', { name: 'Start a chat' }).click();
    await expect(page).toHaveURL(/\/chat\/new$/);
    await expect(page.getByRole('heading', { level: 1, name: 'New chat' })).toBeVisible();
  });

  test('opens a chat whose reply thought first and shares its seq with that activity', async ({ page }) => {
    const options: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/composer-options')) options.push(request.url());
    });
    await openChat(page, '/chat/11');
    await expect(page.getByRole('heading', { level: 1, name: 'Check the shed roof' })).toBeVisible();
    await expect(transcript(page).getByText('Does the shed roof need new sheets?')).toBeVisible();
    await expect(transcript(page).getByText('roof sheets need replacing.', { exact: false })).toBeVisible();
    await expect(transcript(page).getByRole('listitem')).toHaveCount(3);
    const thought = transcript(page).locator('details.thinking');
    await expect(thought).toHaveJSProperty('open', false);
    await thought.getByText('Thinking').click();
    await expect(thought.getByText('Two sheets are cracked in the photos.')).toBeVisible();
    await expect(page.getByText('Loading the chat')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Sonnet 5.5 · high' })).toBeVisible();
    expect(options).toEqual([]);
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [503], allowConsole: unreachableConsole });

    test('shows the error state with a retry', async ({ page }) => {
      await openChat(page, '/chat', { listError: true });
      await expect(page.getByRole('heading', { name: 'Chats could not be loaded' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    });

    test('shows the unreachable state when atrium does not answer', async ({ page }) => {
      await openChat(page, '/chat', { listError: true });
      await expect(page.getByRole('heading', { name: 'Chats could not be loaded' })).toBeVisible();
      await page.evaluate(() => {
        history.pushState({}, '', '/');
        dispatchEvent(new PopStateEvent('popstate'));
      });
      await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeVisible();
      await setUnreachable(page, true);
      await page.evaluate(() => {
        history.pushState({}, '', '/chat');
        dispatchEvent(new PopStateEvent('popstate'));
      });
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' })).toBeVisible();
      await setUnreachable(page, false);
    });
  });
});

test.describe('while atrium is unreachable', () => {
  test.use({ allowConsole: unreachableConsole });

  function writes(page: Page): string[] {
    const seen: string[] = [];
    page.on('request', (request) => {
      if (request.method() !== 'GET') seen.push(`${request.method()} ${new URL(request.url()).pathname}`);
    });
    return seen;
  }

  function transcriptReads(page: Page, thread: number): () => number {
    let count = 0;
    page.on('requestfinished', (request) => {
      if (new URL(request.url()).pathname.endsWith(`/threads/${thread}`)) count++;
    });
    return () => count;
  }

  async function readsSettled(page: Page, reads?: () => number): Promise<void> {
    await expect(page.getByRole('button', { name: 'Sonnet 5.5 · high' })).toBeVisible();
    if (!reads) return;
    await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0);
    await expect.poll(reads).toBe(2);
  }

  test('a message is refused at once, kept in the composer and never sent later', async ({ page }) => {
    const sent = writes(page);
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await readsSettled(page);
    await goUnreachable(page);
    await composer(page).fill('Book the fitting for Saturday');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.locator('.composer').getByRole('alert')).toContainText(
      'Cannot reach atrium. Nothing was sent. The message was kept.',
    );
    await expect(composer(page)).toHaveValue('Book the fitting for Saturday');
    await goReachable(page);
    await page.waitForTimeout(1_000);
    expect(sent).toEqual([]);
  });

  test('an answer is refused at once and never sent later', async ({ page }) => {
    const sent = writes(page);
    const reads = transcriptReads(page, 10);
    await openChat(page, '/chat/10');
    await expect(transcript(page).getByText('Question for you')).toBeVisible();
    await readsSettled(page, reads);
    await goUnreachable(page);
    const box = page.getByRole('textbox', { name: 'Answer' });
    await box.fill('Sage green, please');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Cannot reach atrium. Nothing was sent.' })).toBeVisible();
    await expect(box).toHaveValue('Sage green, please');
    await goReachable(page);
    await page.waitForTimeout(1_000);
    expect(sent).toEqual([]);
  });

  test('approving or declining an ask is refused at once and never sent later', async ({ page }) => {
    test.setTimeout(45_000);
    const sent = writes(page);
    const reads = transcriptReads(page, 8);
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/8');
    const card = transcript(page).locator('[data-approval="501"]');
    await showRow(card);
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    await readsSettled(page, reads);
    await goUnreachable(page);
    await card.getByRole('button', { name: 'Decline' }).click();
    await expect(page.getByText('Cannot reach atrium. Nothing was sent.').first()).toBeVisible();
    await expect(card.getByRole('button', { name: 'Decline' })).toBeVisible();
    await card.getByRole('button', { name: /send email/i }).click();
    const confirm = page.getByRole('dialog', { name: 'Send this now?' });
    if (await confirm.isVisible()) await confirm.getByRole('button', { name: 'Send now' }).click();
    await expect(card.getByRole('button', { name: /send email/i })).toBeEnabled({ timeout: 15_000 });
    await goReachable(page);
    await page.waitForTimeout(1_000);
    expect(sent).toEqual([]);
    await expect(card.getByRole('button', { name: 'Decline' })).toBeVisible();
  });

  test('resuming an interrupted run is refused at once and never sent later', async ({ page }) => {
    const sent = writes(page);
    const reads = transcriptReads(page, 12);
    await openChat(page, '/chat/12');
    const resume = page.getByRole('button', { name: 'Continue', exact: true });
    await expect(resume).toBeVisible();
    await readsSettled(page, reads);
    await goUnreachable(page);
    await resume.click();
    await expect(transcript(page).getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    await expect(resume).toBeVisible();
    expect(sent).toEqual([]);
    await goReachable(page);
    await resume.click();
    await expect(transcript(page).getByText('so I did not place it again', { exact: false })).toBeVisible();
    expect(sent).toEqual(['POST /web/v1/threads/12/runs/106/resume']);
  });

  test('the chat list and the transcript are kept on the device', async ({ page }) => {
    await openChat(page, '/chat/7');
    await expect(transcript(page).getByText('What do twelve cedar boards cost around Hillfield?')).toBeVisible();
    await page.getByRole('link', { name: 'All chats' }).click();
    await expect(page.getByRole('list', { name: 'Chats' })).toBeVisible({ timeout: 10_000 });
    await expect.poll(() => storedKeys(page)).toEqual(expect.arrayContaining(['["threads"]', '["thread",7]']));
    expect((await storedKeys(page)).filter((key) => key.startsWith('["session'))).toEqual([]);
  });

  test.describe('with an unknown outcome', () => {
    test.use({ allowStatus: [409] });

    test('a send whose outcome is unknown is never sent again and is marked once the operator checks', async ({
      page,
    }) => {
      test.setTimeout(45_000);
      const sent = writes(page);
      await page.setViewportSize({ width: desktop.width, height: desktop.height });
      await openChat(page, '/chat/8', { approveUnknown: true });
      const card = transcript(page).locator('[data-approval="501"]');
      await showRow(card);
      const send = card.getByRole('button', { name: /send email/i });
      await expect(send).not.toHaveAttribute('aria-disabled');
      await send.click();
      await expect(
        page
          .getByRole('list', { name: 'Notifications' })
          .getByText('The outcome is unknown. The mail server did not confirm the send. atrium did not try again.'),
      ).toBeVisible({ timeout: 15_000 });
      await expect(card).toHaveAttribute('data-state', 'blocked');
      await expect(card).toContainText('it is unknown whether it happened');
      await expect(card.getByRole('button', { name: /send email/i })).toHaveCount(0);
      const done = card.getByRole('button', { name: 'Mark as done' });
      await expect(done).toBeVisible();
      expect(sent).toEqual(['POST /web/v1/approvals/501/approve']);

      await reconnect(page, /\/web\/v1\/proposals\/501$/);
      await expect(card).toHaveAttribute('data-state', 'blocked');
      expect(sent).toEqual(['POST /web/v1/approvals/501/approve']);
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoSeriousAxeFindings(page);

      await done.click();
      const dialog = page.getByRole('dialog', { name: 'Mark this change as done?' });
      await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
      await dialog.getByRole('button', { name: 'Mark as done' }).click();
      await expect(transcript(page).getByRole('heading', { name: 'Marked as done' })).toBeVisible();
      await expect(card).toHaveCount(0);
      expect(sent).toEqual(['POST /web/v1/approvals/501/approve', 'POST /web/v1/approvals/501/resolve']);
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoSeriousAxeFindings(page);
    });
  });
});

test.describe('streaming', () => {
  test('a run streams text and tool cards to the end, once', async ({ page }) => {
    await openChat(page, '/chat/7');
    const messages = transcript(page);
    await expect(messages.getByText('What do twelve cedar boards cost around Hillfield?')).toBeVisible();
    await expect(messages.getByRole('button', { name: /search_web/ })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Stop run' })).toBeVisible();
    await expect(messages.getByText(finalText)).toBeVisible({ timeout: 15_000 });
    await expect(messages.locator('strong', { hasText: 'twelve' })).toBeVisible();
    await settled(page);
    await page.waitForTimeout(500);
    await expect(messages.getByText(finalText)).toHaveCount(1);
    await expect(messages.getByRole('button', { name: /search_web/ })).toHaveCount(1);
  });

  test('live thinking opens while it streams, the status row names every phase and settled thinking stays collapsed', async ({
    page,
  }) => {
    await page.addInitScript(() => {
      const seen: string[] = [];
      Reflect.set(globalThis, 'atriumPhases', seen);
      const sample = (): void => {
        const text = document.querySelector('.transcript .status-slot')?.textContent?.trim() ?? '';
        if (seen.at(-1) !== text) seen.push(text);
        requestAnimationFrame(sample);
      };
      requestAnimationFrame(sample);
    });
    await openChat(page, '/chat/7', { speed: 1 });
    const messages = transcript(page);
    const thought = messages.locator('details.thinking');
    await expect(thought).toHaveJSProperty('open', true);
    await expect(thought.getByText('Checking the saved supplier list.')).toBeVisible();
    await expect(messages.locator('.status-slot')).toHaveText('Thinking');
    await expect(messages.locator('.status-slot')).toHaveText('Running search_web');
    await expect(thought).toHaveJSProperty('open', false);
    await expect(messages.locator('.tool[data-state="running"] .tool__mark')).toHaveCSS('animation-name', 'pulse');
    await expect(messages.getByText(finalText)).toBeVisible({ timeout: 15_000 });
    await settled(page);
    const phases = (await page.evaluate(() => Reflect.get(globalThis, 'atriumPhases'))) as string[];
    const named = phases.filter((phase) => phase !== 'Connection lost. Reconnecting');
    expect(named.slice(named.indexOf('Thinking'))).toEqual([
      'Thinking',
      'Running search_web',
      'Thinking',
      'Writing',
      '',
    ]);
    await expect(messages.locator('details.thinking')).toHaveCount(1);
    await expect(messages.locator('details.thinking')).toHaveJSProperty('open', false);
  });

  test('a thinking block the reader opened stays open as the run moves on and its transcript settles', async ({
    page,
  }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/7', { speed: 0.5 });
    const messages = transcript(page);
    const thought = messages.locator('details.thinking');
    await expect(thought).toHaveJSProperty('open', true, { timeout: 15_000 });
    await expect(messages.locator('.status-slot')).toHaveText('Running search_web', { timeout: 15_000 });
    await expect(thought).toHaveJSProperty('open', false);
    await thought.getByText('Thinking').click();
    await expect(thought).toHaveJSProperty('open', true);
    await expect(messages.getByText(finalText)).toBeVisible({ timeout: 30_000 });
    await settled(page);
    await page.waitForTimeout(500);
    await expect(thought).toHaveCount(1);
    await expect(thought).toHaveJSProperty('open', true);
    await expect(thought.getByText('Checking the saved supplier list.')).toBeVisible();
    await expectRoundedSurfaces(page, edgeSurfaces);
    await expectNoHorizontalScroll(page);
    await expectNoSeriousAxeFindings(page);
  });

  for (const layout of layouts) {
    test(`${layout.name}: Expand thinking opens every thinking block and stays on after a reload`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/11');
      const thought = transcript(page).locator('details.thinking');
      await expect(thought).toHaveJSProperty('open', false);
      await page.getByRole('button', { name: 'Chat actions' }).click();
      await page.getByRole('menuitem', { name: 'Expand thinking' }).click();
      await expect(thought).toHaveJSProperty('open', true);
      await page.reload();
      await expect(transcript(page).getByText('roof sheets need replacing.', { exact: false })).toBeVisible();
      await expect(thought).toHaveJSProperty('open', true);
      await expect(thought.getByText('Two sheets are cracked in the photos.')).toBeVisible();
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
      await page.getByRole('button', { name: 'Chat actions' }).click();
      await page.getByRole('menuitem', { name: 'Collapse thinking' }).click();
      await expect(thought).toHaveJSProperty('open', false);
    });
  }

  test('the account menu marks Chats while a run works and clears it after', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/7', { speed: 2 });
    const account = page.getByRole('button', { name: /^Account: Operator/ });
    await expect(account).toHaveAccessibleName('Account: Operator, a chat is working');
    await account.click();
    await expect(page.getByRole('menuitem', { name: /^Chats/ })).toHaveAccessibleName(/a chat is working/);
    await page.keyboard.press('Escape');
    await expect(transcript(page).getByText(finalText)).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(account).toHaveAccessibleName('Account: Operator');
  });

  test('the phone Chats tab marks a working run and clears it after', async ({ page }) => {
    const phone = layouts[0] ?? { name: 'phone', width: 390, height: 844, touch: true };
    await page.setViewportSize({ width: phone.width, height: phone.height });
    await openChat(page, '/chat/7', { speed: 2 });
    const tab = page.getByRole('navigation', { name: 'Tabs' }).getByRole('link', { name: /^Chats/ });
    await expect(tab).toHaveAccessibleName(/a chat is working/);
    await expect(tab.locator('.run-dot')).toBeVisible();
    await expect(transcript(page).getByText(finalText)).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(tab.locator('.run-dot')).toHaveCount(0);
  });

  test('a dropped stream resumes from the last event without duplicates', async ({ page }) => {
    const cursors: (string | null)[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (/\/threads\/7\/runs\/101\/events$/.test(url.pathname) && !url.searchParams.has('speed')) {
        cursors.push(request.headers()['last-event-id'] ?? null);
      }
    });
    await openChat(page, '/chat/7', { faults: ['drop:4'] });
    const messages = transcript(page);
    await expect(messages.getByText(finalText)).toBeVisible({ timeout: 15_000 });
    expect(cursors.slice(0, 2)).toEqual([null, '4']);
    await expect(messages.getByRole('button', { name: /search_web/ })).toHaveCount(1);
    await settled(page);
    await expect(messages.getByText(finalText)).toHaveCount(1);
  });

  test.describe('stream limits', () => {
    test.use({ allowStatus: [410, 429] });

    test('a busy server makes the stream wait, then it continues', async ({ page }) => {
      await openChat(page, '/chat/7', { faults: ['busy'] });
      await expect(page.getByText('Too many chats are streaming. Trying again soon')).toBeVisible();
      await expect(transcript(page).getByText(finalText)).toBeVisible({ timeout: 15_000 });
    });

    test('an expired stream reloads the transcript instead', async ({ page }) => {
      await openChat(page, '/chat/7', { faults: ['expired'] });
      const messages = transcript(page);
      await expect(messages.getByText(finalText)).toBeVisible();
      await expect(messages.getByRole('button', { name: /search_web/ })).toHaveCount(1);
      await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0);
    });
  });

  test.describe('lost streams', () => {
    test.use({ allowConsole: unreachableConsole });

    test('a stream that closes without done ends finished after a failed reconnect', async ({ page }) => {
      const cursors: (string | null)[] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (/\/threads\/7\/runs\/101\/events$/.test(url.pathname) && !url.searchParams.has('speed')) {
          cursors.push(request.headers()['last-event-id'] ?? null);
        }
      });
      await openChat(page, '/chat/7', { faults: ['nodone', 'error'] });
      const messages = transcript(page);
      await expect(messages.getByText(finalText)).toBeVisible({ timeout: 15_000 });
      await settled(page);
      expect(cursors).toEqual([null, '9', '9']);
      await expect(page.getByText(/Connection lost/)).toHaveCount(0);
      await expect(messages.getByText(finalText)).toHaveCount(1);
    });
  });

  test('a parked run shows the ask inside the waiting tool card and approves', async ({ page }) => {
    test.setTimeout(45_000);
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/8');
    const messages = transcript(page);
    await expect(messages.getByText('The reminder is ready to send to 6 people.')).toBeVisible();
    const card = messages.locator('[data-approval="501"]');
    await expect(card).toBeVisible();
    await expect(card.getByRole('heading', { name: 'Send the book club reminder to 6 people' })).toBeVisible();
    await expect(messages.getByRole('button', { name: 'Waiting for you mail.send' })).toBeVisible();
    await showRow(card);
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    const send = card.getByRole('button', { name: /send email/i });
    await send.focus();
    await page.keyboard.press('Enter');
    const confirm = page.getByRole('dialog', { name: 'Send this now?' });
    if (await confirm.isVisible()) await confirm.getByRole('button', { name: 'Send now' }).click();
    await expect(messages.getByText('Sent the reminder to 6 people.')).toBeVisible({ timeout: 20_000 });
  });

  for (const layout of layouts) {
    test(`${layout.name}: Decline sends the dismissal at once`, async ({ page }) => {
      test.setTimeout(45_000);
      const sent: string[] = [];
      page.on('request', (request) => {
        if (request.method() !== 'GET') sent.push(`${request.method()} ${new URL(request.url()).pathname}`);
      });
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/8');
      const card = transcript(page).locator('[data-approval="501"]');
      await showRow(card);
      await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
      await card.getByRole('button', { name: 'Decline' }).click();
      await expect.poll(() => sent, { timeout: 2_000 }).toEqual(['POST /web/v1/approvals/501/deny']);
      await expect(transcript(page).getByText('Understood. The reminder was not sent.')).toBeVisible({
        timeout: 20_000,
      });
      await expect(transcript(page).getByRole('heading', { name: 'Declined' })).toBeVisible();
      await expect(transcript(page).getByRole('button', { name: 'Undo', exact: true })).toHaveCount(0);
      expect(sent).toEqual(['POST /web/v1/approvals/501/deny']);
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });
  }

  test('an approved download reads as applied with Undo, and Undo reverses it', async ({ page }) => {
    test.setTimeout(45_000);
    const sent: string[] = [];
    page.on('request', (request) => {
      if (request.method() !== 'GET') sent.push(`${request.method()} ${new URL(request.url()).pathname}`);
    });
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/13');
    const messages = transcript(page);
    const card = messages.locator('[data-approval="502"]');
    await showRow(card);
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    await card.getByRole('button', { name: 'Add torrent' }).click();
    const confirm = page.getByRole('dialog', { name: 'Send this now?' });
    if (await confirm.isVisible()) await confirm.getByRole('button', { name: 'Send now' }).click();
    await expect(messages.getByRole('heading', { name: 'Approved and applied' })).toBeVisible({ timeout: 20_000 });
    await expect(messages.getByText('Add the Debian netinstall torrent')).toBeVisible();
    await expect(messages.getByRole('heading', { name: 'Declined' })).toHaveCount(0);
    const undo = messages.getByRole('button', { name: 'Undo', exact: true });
    await expect(undo).toBeVisible();
    await expectNoHorizontalScroll(page);
    await expectRoundedSurfaces(page, edgeSurfaces);
    await expectNoSeriousAxeFindings(page);

    await undo.click();
    await expect(messages.getByRole('heading', { name: 'Approved, then undone' })).toBeVisible();
    await expect(undo).toHaveCount(0);
    expect(sent).toEqual(['POST /web/v1/approvals/502/approve', 'POST /web/v1/effects/1202/undo']);
  });

  test('Stop ends the run and says what happened', async ({ page }) => {
    await openChat(page, '/chat/50', { speed: 4 });
    await expect(
      transcript(page)
        .getByRole('button', { name: /search_web/ })
        .first(),
    ).toBeVisible();
    await page.getByRole('button', { name: 'Stop run' }).click();
    await expect(
      page.getByRole('list', { name: 'Notifications' }).getByText('Stopped. Steps already done stay done.'),
    ).toBeVisible();
    await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0);
    await expect(transcript(page).getByText('Stopped.', { exact: true })).toBeVisible();
  });
});

test.describe('composer', () => {
  test('Send stays disabled until the transcript has loaded', async ({ page }) => {
    await openChat(page, '/chat/9', { transcriptDelay: 1500 });
    await composer(page).fill('Is 175/65 R14 right?');
    await expect(page.getByRole('button', { name: 'Send' })).toBeDisabled();
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Send' })).toBeEnabled();
  });

  test('a question from the assistant is answered from the composer', async ({ page }) => {
    const answers: unknown[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && request.url().endsWith('/threads/10/answer')) {
        answers.push(request.postDataJSON());
      }
    });
    await openChat(page, '/chat/10');
    const messages = transcript(page);
    await expect(messages.getByText('Question for you')).toBeVisible();
    await expect(messages.locator('strong', { hasText: 'sage green' })).toBeVisible();
    const box = page.getByRole('textbox', { name: 'Answer' });
    await box.fill('Sage green, please');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(box).toHaveCount(0);
    await expect(composer(page)).toHaveValue('');
    expect(answers).toEqual([{ text: 'Sage green, please' }]);
    await expect(messages.getByText('Question for you')).toHaveCount(0);
    await expect(messages.getByText('Sage green, please', { exact: true })).toBeVisible();
    await expect(messages.getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
  });

  test('Enter sends on a desktop, but never during IME composition', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    const box = composer(page);
    await box.fill('Which shop is closest?');
    await box.evaluate((element) => {
      element.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true }),
      );
    });
    await page.waitForTimeout(300);
    await expect(box).toHaveValue('Which shop is closest?');
    await box.press('Shift+Enter');
    await expect(box).toHaveValue('Which shop is closest?\n');
    await box.press('Enter');
    await expect(box).toHaveValue('');
    const messages = transcript(page);
    await expect(messages.getByText('Which shop is closest?')).toBeVisible();
    await expect(messages.getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(messages.getByText('Which shop is closest?', { exact: true })).toHaveCount(1);
  });

  test('Enter makes a new line on a touch layout and Send sends', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    const box = composer(page);
    await box.fill('First line');
    await box.press('Enter');
    await expect(box).toHaveValue('First line\n');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(box).toHaveValue('');
    await expect(transcript(page).getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
  });

  test.describe('send failures', () => {
    test.use({ allowStatus: [503] });

    test('a failed send keeps the text and retries', async ({ page }) => {
      await openChat(page, '/chat/9', { failSends: 1 });
      await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
      await composer(page).fill('Book the fitting for Saturday');
      await page.getByRole('button', { name: 'Send' }).click();
      await expect(page.locator('.composer').getByRole('alert')).toContainText('The assistant is not answering.');
      await expect(composer(page)).toHaveValue('Book the fitting for Saturday');
      await expect(transcript(page).getByText('Book the fitting for Saturday')).toHaveCount(0);
      await page.getByRole('button', { name: 'Retry' }).click();
      await expect(composer(page)).toHaveValue('');
      await expect(transcript(page).getByText('Book the fitting for Saturday')).toBeVisible();
    });
  });

  test('a scope change is staged with its price and applies from the next message', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    const patches: unknown[] = [];
    page.on('request', (request) => {
      if (request.method() === 'PATCH' && request.url().includes('/threads/9/options')) {
        patches.push(request.postDataJSON());
      }
    });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Sonnet 5.5 · high' }).click();
    await page.getByRole('menuitem', { name: /^Opus 5\.5/ }).click();
    await expect(
      page.getByRole('button', {
        name: 'Opus 5.5 · high · 200k, from the next message, $75 per million output tokens',
      }),
    ).toBeVisible();
    expect(patches).toEqual([]);
    await composer(page).fill('Compare the two shops');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByRole('button', { name: 'Opus 5.5 · high · 200k', exact: true })).toBeVisible();
    expect(patches).toEqual([{ model: 'claude-opus-5-5', effort: 'high', context_limit: 200_000 }]);
  });

  test('the context size is chosen from the composer and applies from the next message', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    const patches: unknown[] = [];
    page.on('request', (request) => {
      if (request.method() === 'PATCH' && request.url().includes('/threads/9/options')) {
        patches.push(request.postDataJSON());
      }
    });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Sonnet 5.5 · high · 200k' }).click();
    await expect(page.getByRole('menuitem', { name: 'Context 90k' })).toBeVisible();
    await page.getByRole('menuitem', { name: 'Context 1M' }).click();
    await composer(page).fill('Read the whole manual');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByRole('button', { name: 'Sonnet 5.5 · high · 1M', exact: true })).toBeVisible();
    expect(patches).toEqual([{ model: 'claude-sonnet-5-5', effort: 'high', context_limit: 1_000_000 }]);
  });

  test('a new chat starts from the composer and opens its page', async ({ page }) => {
    await openChat(page, '/chat/new');
    await expect(page.getByRole('heading', { name: 'Start a chat' })).toBeVisible();
    await composer(page).fill('Plan a picnic for Sunday');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page).toHaveURL(/\/chat\/2\d\d$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Plan a picnic for Sunday' })).toBeVisible();
    await expect(transcript(page).getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
  });

  test('a live refresh keeps the unsent draft', async ({ page }) => {
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await composer(page).fill('Half-written question');
    const refreshed = page.waitForResponse((response) => /\/threads\/9\?/.test(response.url()));
    await page.evaluate(() => {
      const channel = new BroadcastChannel('atrium-mock-events');
      channel.postMessage({ topic: 'assistant' });
      channel.close();
    });
    await refreshed;
    await expect(composer(page)).toHaveValue('Half-written question');
    await expect(composer(page)).toBeFocused();
  });
});

test.describe('the composer with a lost answer', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height }, allowConsole: unreachableConsole });

  test('a scope change whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'PATCH',
      path: '/threads/9/options',
      open: '/chat/9',
      scenario: { chat: { speed: 8 } },
      press: async (page) => {
        await page.getByRole('button', { name: 'Sonnet 5.5 · high' }).click();
        await page.getByRole('menuitem', { name: /^Opus 5\.5/ }).click();
        await composer(page).fill('Compare the two shops');
        await page.getByRole('button', { name: 'Send' }).click();
      },
    });
  });

  test('a new chat whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/threads',
      open: '/chat/new',
      scenario: { chat: { speed: 8 } },
      press: async (page) => {
        await composer(page).fill('Plan a picnic for Sunday');
        await page.getByRole('button', { name: 'Send' }).click();
      },
    });
  });

  test('a message whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/threads/9/messages',
      open: '/chat/9',
      scenario: { chat: { speed: 8 } },
      press: async (page) => {
        await composer(page).fill('Book the fitting for Saturday');
        await page.getByRole('button', { name: 'Send' }).click();
      },
    });
  });

  test('an answer whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/threads/10/answer',
      open: '/chat/10',
      press: async (page) => {
        await expect(transcript(page).getByText('Question for you')).toBeVisible();
        await page.getByRole('textbox', { name: 'Answer' }).fill('Sage green, please');
        await page.getByRole('button', { name: 'Send' }).click();
      },
    });
  });
});

test.describe('scrolling', () => {
  test('follows at the bottom, stops when scrolled up and offers a jump', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/50', { speed: 10 });
    await expect.poll(async () => (await scrollState(page)).y, { timeout: 20_000 }).toBeGreaterThan(400);
    expect((await scrollState(page)).gap).toBeLessThanOrEqual(80);
    await page.mouse.move(700, 300);
    await page.mouse.wheel(0, -400);
    const jump = page.getByRole('button', { name: 'Jump to latest' });
    await expect(jump).toBeVisible();
    await expect
      .poll(async () => {
        const before = (await scrollState(page)).y;
        await page.waitForTimeout(150);
        return Math.abs((await scrollState(page)).y - before);
      })
      .toBeLessThanOrEqual(1);
    const held = (await scrollState(page)).y;
    await page.waitForTimeout(800);
    expect(Math.abs((await scrollState(page)).y - held)).toBeLessThanOrEqual(1);
    await jump.click();
    await expect.poll(async () => (await scrollState(page)).gap).toBeLessThanOrEqual(80);
    await expect(jump).toHaveCount(0);
  });

  test('going back restores the place in a long chat', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await expect
      .poll(async () => {
        await page.evaluate(() => window.scrollTo({ top: 900, behavior: 'instant' }));
        await page.waitForTimeout(150);
        return page.evaluate(() => sessionStorage.getItem('atrium.chat.scroll.9'));
      })
      .toBe('900');
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeAttached();
    await page.goBack();
    await expect(page.getByRole('heading', { level: 1, name: 'Winter tyre sizes' })).toBeVisible();
    await expect.poll(async () => Math.round((await scrollState(page)).y)).toBe(900);
  });

  test('earlier messages load above without moving the reader', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await expect(transcript(page).getByText('Question 1:', { exact: false })).toHaveCount(0);
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }));
    const anchor = transcript(page).getByText('Question 16:', { exact: false });
    const before = await anchor.boundingBox();
    await page.getByRole('button', { name: 'Show earlier messages' }).click();
    await expect(transcript(page).getByText('Question 1:', { exact: false })).toHaveCount(1);
    const after = await anchor.boundingBox();
    expect(Math.abs((after?.y ?? 0) - (before?.y ?? 0))).toBeLessThanOrEqual(2);
  });
});

test.describe('an interrupted run', () => {
  const resumed = 'The order for twelve boards went through, so I did not place it again.';

  function resumes(page: Page): (string | undefined)[] {
    const keys: (string | undefined)[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && request.url().endsWith('/threads/12/runs/106/resume'))
        keys.push(request.headers()['idempotency-key']);
    });
    return keys;
  }

  test('names the uncertain change, offers Continue instead of Stop and drains the queue once', async ({ page }) => {
    const keys = resumes(page);
    await openChat(page, '/chat/12');
    const notice = transcript(page).getByText('This run stopped during a change');
    await expect(notice).toBeVisible();
    await expect(transcript(page).getByText('order_supplies {"boards": 12}')).toBeVisible();
    await expect(transcript(page).getByText('I will order twelve cedar boards from Hillfield Timber.')).toBeVisible();
    const queued = transcript(page).getByText('Tell me the delivery date too.');
    await expect(queued).toBeVisible();
    await expect(transcript(page).getByText('Queued', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0);
    await expect(page.getByText('The run failed')).toHaveCount(0);
    await page.getByRole('button', { name: 'Continue', exact: true }).click();
    await expect(transcript(page).getByText(resumed)).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(notice).toHaveCount(0);
    await expect(queued).toHaveCount(1);
    await expect(transcript(page).getByText('Queued', { exact: true })).toHaveCount(0);
    expect(keys).toEqual([expect.stringMatching(/^[0-9a-f-]{36}$/)]);
  });

  test.describe('with a lost connection', () => {
    test.use({ allowConsole: unreachableConsole });

    test('a network failure says the outcome is unknown and does not try again', async ({ page }) => {
      const keys = resumes(page);
      await openChat(page, '/chat/12', { resumeFault: true });
      await page.getByRole('button', { name: 'Continue', exact: true }).click();
      await expect(transcript(page).getByText('The outcome is unknown')).toBeVisible();
      await expect(transcript(page).getByText(resumed)).toBeVisible({ timeout: 15_000 });
      expect(keys).toHaveLength(1);
    });
  });

  for (const layout of layouts) {
    test(`${layout.name}: the interrupted run passes the design and axe checks`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/12');
      await expect(page.getByText('order_supplies {"boards": 12}')).toBeVisible();
      await expect(page.getByRole('button', { name: 'Continue', exact: true })).toBeVisible();
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });
  }
});

test.describe('a stopped run', () => {
  const reason = 'The run stopped after 24 steps, the most one chat run may take.';

  test('names why it stopped and Continue starts a new run in the same chat', async ({ page }) => {
    const sent: string[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && request.url().endsWith('/threads/14/messages'))
        sent.push(String(request.postDataJSON()?.text));
    });
    await openChat(page, '/chat/14');
    const notice = transcript(page).getByText(reason);
    await expect(notice).toBeVisible();
    await expect(page.getByText('The run failed')).toHaveCount(0);
    await page.getByRole('button', { name: 'Continue', exact: true }).click();
    await expect(transcript(page).getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(notice).toBeVisible();
    await expect(page.getByRole('button', { name: 'Continue', exact: true })).toHaveCount(0);
    expect(sent).toEqual(['Continue.']);
    await expect(page).toHaveURL(/\/chat\/14$/);
  });

  for (const layout of layouts) {
    test(`${layout.name}: the stopped run passes the design and axe checks`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/14');
      await expect(transcript(page).getByText(reason)).toBeVisible();
      await expect(page.getByRole('button', { name: 'Continue', exact: true })).toBeVisible();
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });
  }
});

test.describe('design checks', () => {
  for (const layout of galleryLayouts) {
    test(`${layout.name}: the list, a finished run and a parked run pass the design and axe checks`, async ({
      page,
    }) => {
      test.setTimeout(60_000);
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await openChat(page, '/chat', { speed: 20 });
      await expect(page.getByRole('list', { name: 'Chats' })).toBeVisible();
      for (const path of ['/chat', '/chat/7', '/chat/8']) {
        if (path !== '/chat') await gotoShell(page, path);
        if (path === '/chat/7') {
          await expect(transcript(page).getByText(finalText)).toBeVisible({ timeout: 15_000 });
          await settled(page);
        }
        if (path === '/chat/8') {
          const card = transcript(page).locator('[data-approval="501"]');
          await expect(card).toBeVisible();
          await showRow(card);
          await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
          await card.evaluate((element) =>
            Promise.all(
              element
                .getAnimations({ subtree: true })
                .filter((animation) => animation.effect?.getComputedTiming().endTime !== Infinity)
                .map((animation) => animation.finished),
            ),
          );
        }
        if (layout.touch) await expectTouchTargets(page, 'main');
        await expectRoundedSurfaces(page, edgeSurfaces);
        await expectNoHorizontalScroll(page);
        if (path === '/chat/7')
          await page.evaluate(() =>
            window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' }),
          );
        await expectNoSeriousAxeFindings(page);
      }
    });

    test(`${layout.name}: a working run with live thinking passes the design and axe checks`, async ({ page }) => {
      test.setTimeout(60_000);
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/7', { speed: 0.2 });
      await expect(transcript(page).locator('details.thinking')).toHaveJSProperty('open', true, { timeout: 15_000 });
      await expect(transcript(page).locator('.status-slot')).toHaveText('Thinking');
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });

    if (layout.touch && layout.width < 861) {
      test(`${layout.name}: the composer starts above the fold`, async ({ page }) => {
        await page.setViewportSize({ width: layout.width, height: layout.height });
        await openChat(page, '/chat/9');
        await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
        const box = await page.locator('.composer').boundingBox();
        const bar = await page.locator('.tabbar').boundingBox();
        expect(box && bar && box.y + box.height <= bar.y).toBe(true);
        expect(box?.height ?? 0).toBeLessThanOrEqual(50);
      });
    }
  }
});

test.describe('G3', () => {
  test.skip(process.platform !== 'darwin', 'G3 is measured on the Mac only');

  test('2,000 events at 100 per second keep p95 apply time at or under 50 ms', async ({ page, browserName }) => {
    test.setTimeout(120_000);
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await page.addInitScript(() => {
      const stamps: number[] = [];
      const tick = (now: number): void => {
        stamps.push(now);
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
      Object.assign(window, { atriumFrameStamps: stamps });
    });
    await openChat(page, '/chat/50', { liveStream: true });
    await expect(transcript(page).getByRole('heading', { name: 'Garage 79' })).toBeVisible({ timeout: 90_000 });
    await settled(page);
    const result = await page.evaluate(() => {
      const quantile = (values: number[], q: number): number => {
        const sorted = [...values].sort((a, b) => a - b);
        return sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))] ?? 0;
      };
      const entries = performance.getEntriesByName('atrium-run-apply');
      const applies = entries.map((entry) => entry.duration);
      const from = entries[0]?.startTime ?? 0;
      const to = entries.at(-1)?.startTime ?? 0;
      const stamps = (window as unknown as { atriumFrameStamps: number[] }).atriumFrameStamps.filter(
        (stamp) => stamp >= from && stamp <= to,
      );
      const gaps = stamps.slice(1).map((stamp, index) => stamp - (stamps[index] ?? stamp));
      return {
        frames: applies.length,
        applyP50: quantile(applies, 0.5),
        applyP95: quantile(applies, 0.95),
        applyMax: Math.max(...applies),
        frameP95: quantile(gaps, 0.95),
      };
    });
    console.log(`G3 ${browserName}: ${JSON.stringify(result)}`);
    test.info().annotations.push({ type: 'G3', description: `${browserName} ${JSON.stringify(result)}` });
    expect(result.frames).toBeGreaterThan(50);
    expect(result.applyP95).toBeLessThanOrEqual(50);
  });
});

test.describe('asks from one step', () => {
  const gallery = (page: Page): Locator => page.locator('section.asks');

  for (const layout of layouts) {
    test(`${layout.name}: three asks form one gallery that passes the design and axe checks`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openChat(page, '/chat/15');
      await expect(gallery(page).getByText('3 asks')).toBeVisible();
      await expect(gallery(page).locator('.asks__card')).toHaveCount(3);
      await expect(gallery(page).getByText('Behind the boiler.')).toBeVisible();
      await expect(gallery(page).getByText('Under the stairs.')).toBeVisible();
      const track = gallery(page).locator('.asks__track');
      expect(await track.evaluate((element) => getComputedStyle(element).scrollSnapType)).toContain('x');
      await track.focus();
      await expect(track).toBeFocused();
      if (layout.touch) await expectTouchTargets(page, 'main');
      await expectRoundedSurfaces(page, edgeSurfaces);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });
  }

  test('Approve all decides every card and the run resumes once', async ({ page }) => {
    await openChat(page, '/chat/15');
    const approvals: string[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && /\/approvals\/\d+\/approve$/.test(request.url()))
        approvals.push(request.url());
    });
    await gallery(page).getByRole('button', { name: 'Approve all' }).click();
    await expect(page.getByText('Sent the reminder to 6 people.')).toHaveCount(1);
    expect(approvals).toHaveLength(3);
    await expect(gallery(page)).toHaveCount(0);
  });

  test('Deny all declines every card', async ({ page }) => {
    await openChat(page, '/chat/15');
    const denials: string[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && /\/approvals\/\d+\/deny$/.test(request.url())) denials.push(request.url());
    });
    await gallery(page).getByRole('button', { name: 'Deny all' }).click();
    await expect(page.getByText('Understood. The reminder was not sent.')).toHaveCount(1);
    expect(denials).toHaveLength(3);
  });
});
