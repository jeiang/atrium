import type { Page } from '@playwright/test';
import type { ChatScenario } from '../src/mocks/chats.ts';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, galleryLayouts, layouts, test, useScenario } from './fixtures.ts';

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

test.describe('chat list', () => {
  test('lists chats with their run state, pages and opens one', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat');
    await expect(page.getByRole('heading', { level: 1, name: 'Chats' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Chats' });
    await expect(list.getByRole('listitem').filter({ hasText: 'Plan the garden shed order' })).toContainText(
      'Running',
    );
    await expect(list.getByRole('listitem').filter({ hasText: 'Send the book club reminder' })).toContainText(
      'Waiting for you',
    );
    await expect(list.getByRole('listitem')).toHaveCount(30);
    await page.getByRole('button', { name: 'Show more chats' }).click();
    await expect(list.getByRole('listitem')).toHaveCount(38);
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

  test.describe('failures', () => {
    test.use({ allowStatus: [503] });

    test('shows the error state with a retry', async ({ page }) => {
      await openChat(page, '/chat', { listError: true });
      await expect(page.getByRole('heading', { name: 'Chats could not be loaded' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    });

    test('shows the offline state when the connection is gone', async ({ page, context }) => {
      await openChat(page, '/', { listError: true });
      await context.setOffline(true);
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Chats', exact: true }).click();
      await expect(page.getByRole('heading', { name: 'You are offline' })).toBeVisible();
      await context.setOffline(false);
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

  test('a parked run shows the ask inside the waiting tool card and approves', async ({ page }) => {
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/8');
    const messages = transcript(page);
    await expect(messages.getByText('The reminder is ready to send to 6 people.')).toBeVisible();
    const card = messages.locator('[data-approval="501"]');
    await expect(card).toBeVisible();
    await expect(card.getByRole('heading', { name: 'Send the book club reminder to 6 people' })).toBeVisible();
    await expect(page.getByText('Waiting for you')).toBeVisible();
    await card.scrollIntoViewIfNeeded();
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    const send = card.getByRole('button', { name: /send email/i });
    await send.focus();
    await page.keyboard.press('Enter');
    await expect(send).not.toHaveAttribute('aria-disabled');
    await page.keyboard.press('Enter');
    await page.getByRole('dialog', { name: 'Send this now?' }).getByRole('button', { name: 'Send now' }).click();
    await expect(messages.getByText('Sent the reminder to 6 people.')).toBeVisible();
    await expect(messages.getByRole('heading', { name: 'Approved' })).toBeVisible();
    await expect(messages.locator('[data-approval="501"]')).toHaveCount(0);
  });

  test('declining waits for the undo window, and Undo keeps the ask open', async ({ page }) => {
    test.setTimeout(45_000);
    await page.setViewportSize({ width: desktop.width, height: desktop.height });
    await openChat(page, '/chat/8');
    const card = transcript(page).locator('[data-approval="501"]');
    await card.scrollIntoViewIfNeeded();
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    await card.getByRole('button', { name: 'Decline' }).click();
    await expect(card.getByText('Declined. atrium will not do this')).toBeVisible();
    await card.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(card.getByRole('button', { name: 'Decline' })).toBeVisible();
    await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
    await card.getByRole('button', { name: 'Decline' }).click();
    await expect(transcript(page).getByText('Understood. The reminder was not sent.')).toBeVisible({
      timeout: 20_000,
    });
    await expect(transcript(page).getByRole('heading', { name: 'Declined' })).toBeVisible();
  });

  test('Stop ends the run and says what happened', async ({ page }) => {
    await openChat(page, '/chat/50', { speed: 4 });
    await expect(transcript(page).getByRole('button', { name: /search_web/ }).first()).toBeVisible();
    await page.getByRole('button', { name: 'Stop run' }).click();
    await expect(page.getByText('Stopped. Steps already done stay done.')).toBeVisible();
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
    await expect(messages.getByText('Which shop is closest?')).toHaveCount(1);
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
      await expect(page.getByRole('alert')).toContainText('The assistant is not answering.');
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
    await page.getByRole('menuitem', { name: /^Opus 5/ }).click();
    await expect(
      page.getByRole('button', { name: 'Opus 5 · high, from the next message, $75 per million output tokens' }),
    ).toBeVisible();
    expect(patches).toEqual([]);
    await composer(page).fill('Compare the two shops');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByRole('button', { name: 'Opus 5 · high', exact: true })).toBeVisible();
    expect(patches).toEqual([{ model: 'opus-5', effort: 'high' }]);
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
    await page.evaluate(() => window.scrollTo({ top: 900, behavior: 'instant' }));
    await page.waitForTimeout(200);
    await page.getByRole('link', { name: 'All chats' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Chats' })).toBeVisible();
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
          await card.scrollIntoViewIfNeeded();
          await expect(card.locator('.approval__row')).not.toHaveAttribute('inert');
        }
        if (layout.touch) await expectTouchTargets(page, 'main');
        await expectRoundedSurfaces(page, edgeSurfaces);
        await expectNoHorizontalScroll(page);
        await expectNoSeriousAxeFindings(page);
      }
    });

    if (layout.touch && layout.width < 861) {
      test(`${layout.name}: the composer starts above the fold`, async ({ page }) => {
        await page.setViewportSize({ width: layout.width, height: layout.height });
        await openChat(page, '/chat/9');
        await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
        const box = await page.locator('.composer').boundingBox();
        const bar = await page.locator('.tabbar').boundingBox();
        expect(box && bar && box.y + box.height <= bar.y).toBe(true);
        expect(box?.height ?? 0).toBeLessThanOrEqual(64);
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
      const gaps: number[] = [];
      let last = 0;
      const tick = (now: number): void => {
        if (last) gaps.push(now - last);
        last = now;
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
      Object.assign(window, { atriumFrameGaps: gaps });
    });
    await openChat(page, '/chat/50', { speed: 40 });
    await expect(transcript(page).getByRole('heading', { name: 'Garage 79' })).toBeVisible({ timeout: 90_000 });
    await settled(page);
    const result = await page.evaluate(() => {
      const quantile = (values: number[], q: number): number => {
        const sorted = [...values].sort((a, b) => a - b);
        return sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))] ?? 0;
      };
      const applies = performance.getEntriesByName('atrium-run-apply').map((entry) => entry.duration);
      const gaps = (window as unknown as { atriumFrameGaps: number[] }).atriumFrameGaps;
      return {
        frames: applies.length,
        applyP50: quantile(applies, 0.5),
        applyP95: quantile(applies, 0.95),
        applyMax: Math.max(...applies),
        gapP95: quantile(gaps, 0.95),
      };
    });
    console.log(`G3 ${browserName}: ${JSON.stringify(result)}`);
    test.info().annotations.push({ type: 'G3', description: `${browserName} ${JSON.stringify(result)}` });
    expect(result.frames).toBeGreaterThan(50);
    expect(result.applyP95).toBeLessThanOrEqual(50);
  });
});
