import type { Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectReducedMotion,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, galleryLayouts, type Layout, test, useScenario } from './fixtures.ts';

const desktop = { width: 1440, height: 900 };
const phone = { viewport: { width: 390, height: 844 }, hasTouch: true };
const homeLayouts: readonly Layout[] = [
  ...galleryLayouts,
  { name: 'narrow desktop', width: 1024, height: 768, touch: false },
  { name: 'wide desktop', width: 1920, height: 1080, touch: false },
];

async function gotoHome(page: Page): Promise<void> {
  await gotoShell(page, '/');
  await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeAttached();
}

function needsYou(page: Page) {
  return page.locator('.home__needs');
}

async function postTopic(page: Page, topic: string, scenario: object): Promise<void> {
  await page.evaluate(
    ([name, value]) => {
      localStorage.setItem('atrium.mock', value);
      const channel = new BroadcastChannel('atrium-mock-events');
      channel.postMessage({ topic: name });
      channel.close();
    },
    [topic, JSON.stringify(scenario)] as const,
  );
}

test.describe('home on desktop', () => {
  test.use({ viewport: desktop });

  test('the composer comes first in the page and in tab order', async ({ page }) => {
    await gotoHome(page);
    const order = await page.evaluate(() =>
      [...document.querySelectorAll('.home > :not(h1)')].map((el) => el.className.split(' ')[0]),
    );
    expect(order[0]).toBe('home__composer');
    await page.locator('main h1').evaluate((h1) => {
      h1.setAttribute('tabindex', '-1');
      (h1 as HTMLElement).focus();
    });
    await page.keyboard.press('Tab');
    await expect(page.getByRole('textbox', { name: 'Message' })).toBeFocused();
  });

  test('compose from Home opens a chat', async ({ page }) => {
    await gotoHome(page);
    const box = page.getByRole('textbox', { name: 'Message' });
    const send = page.getByRole('button', { name: 'Send' });
    await expect(send).toBeDisabled();
    await box.fill('What is on this week?');
    await box.press('Shift+Enter');
    await box.pressSequentially('Keep it short.');
    await box.press('Enter');
    await expect(page).toHaveURL(/\/chat\/\d+$/);
    await expect(page.getByRole('heading', { level: 1, name: /^What is on this week\?/ })).toBeVisible();
    const started = await page.evaluate(() => Reflect.get(globalThis, 'atriumMockStarted'));
    expect(started).toHaveLength(1);
    expect(started[0].body).toEqual({ text: 'What is on this week?\nKeep it short.' });
    expect(started[0].idempotencyKey).toBeTruthy();
  });

  test('weather card from the /home model', async ({ page }) => {
    await gotoHome(page);
    const card = page.locator('.home__weather');
    await expect(card.getByRole('heading', { level: 2, name: 'Weather in Hillfield' })).toBeVisible();
    await expect(card).toContainText('6°');
    await expect(card).toContainText('Overcast');
    await expect(card).toContainText('Feels like 3°');
    await expect(card).toContainText('60%');
    await expect(card).not.toContainText('Last updated');
  });

  test('a stale forecast shows its age', async ({ page }) => {
    await useScenario(page, { home: { weather: 'stale' } });
    await gotoHome(page);
    await expect(page.locator('.home__weather')).toContainText(/Last updated (3 hours|1[5-9]\d minutes) ago/);
  });

  test('no forecast shows one quiet hint with the location setting', async ({ page }) => {
    await useScenario(page, { home: { weather: 'none' } });
    await gotoHome(page);
    const card = page.locator('.home__weather');
    await expect(card.getByRole('heading', { level: 2, name: 'Weather' })).toBeVisible();
    await expect(card.getByRole('link', { name: 'Set a location in the current interface' })).toHaveAttribute(
      'href',
      '/settings/assistant',
    );
    await expect(card).not.toContainText('°');
  });

  test('the assistant off hides the composer, the weather and the digest', async ({ page }) => {
    await useScenario(page, { modules: { assistant: 'off' } });
    await gotoHome(page);
    await expect(page.getByRole('heading', { name: 'The assistant is off' })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Message' })).toHaveCount(0);
    await expect(page.locator('.home__weather, .home__digest')).toHaveCount(0);
    await expect(needsYou(page).getByRole('link')).toHaveCount(4);
  });

  test('the digest shows five lines and links the rest', async ({ page }) => {
    await gotoHome(page);
    const digest = page.locator('.home__digest');
    await expect(digest.getByRole('heading', { level: 2, name: "Today's digest" })).toBeVisible();
    await expect(digest.locator('.digest__list > li')).toHaveCount(5);
    await expect(digest.getByRole('link', { name: 'and 2 more' })).toHaveAttribute('href', '/chat');
  });

  test('an empty day says so', async ({ page }) => {
    await useScenario(page, { needsYou: 0, home: { digest: 0 } });
    await gotoHome(page);
    await expect(needsYou(page).getByRole('heading', { name: 'Nothing needs you' })).toBeVisible();
    await expect(page.locator('.home__digest')).toContainText('No digest yet today');
  });

  test('the Needs you count matches the badge and opens the inbox item', async ({ page }) => {
    await gotoHome(page);
    const rail = page.getByRole('navigation', { name: 'Main' });
    await expect(rail.getByRole('link', { name: /^Inbox\W+3 waiting$/ })).toBeVisible();
    const list = needsYou(page);
    await expect(list).toContainText('3 waiting');
    const rows = list.getByRole('list', { name: 'Waiting for you' }).getByRole('link');
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0)).toHaveAttribute('href', '/inbox/proposal:12');
    await expect(rows.nth(1)).toHaveAttribute('href', '/inbox/proposal:14');
    await expect(rows.nth(2)).toHaveAttribute('href', '/inbox/draft:7');
    await expect(list.getByRole('link', { name: 'All in Inbox' })).toHaveAttribute('href', '/inbox');
    await rows.nth(0).click();
    await expect(page).toHaveURL(/\/inbox\/proposal:12$/);
    await expect(page.getByRole('heading', { name: 'Move the dentist visit to Friday' }).first()).toBeVisible();
  });

  test('many items show five and the full count', async ({ page }) => {
    await useScenario(page, { needsYou: 12 });
    await gotoHome(page);
    const list = needsYou(page);
    await expect(list).toContainText('12 waiting');
    await expect(list.getByRole('list', { name: 'Waiting for you' }).getByRole('link')).toHaveCount(5);
  });

  test('an app event refreshes the list', async ({ page }) => {
    await gotoHome(page);
    const rows = needsYou(page).getByRole('list', { name: 'Waiting for you' }).getByRole('link');
    await expect(rows).toHaveCount(3);
    await expect.poll(() => page.evaluate(() => Number(Reflect.get(globalThis, 'atriumMockStreams') ?? 0))).toBe(1);
    await postTopic(page, 'review', { needsYou: 5 });
    await expect(rows).toHaveCount(5);
    await expect(rows.nth(3)).toContainText('blocked');
    await expect(needsYou(page)).toContainText('5 waiting');
  });

  test('loading reserves the final size', async ({ page, browserName }) => {
    test.skip(browserName !== 'chromium', 'layout-shift entries exist only in Chromium');
    await useScenario(page, { home: { delayMs: 600 } });
    await page.addInitScript(() => {
      Reflect.set(globalThis, 'atriumShift', 0);
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries() as (PerformanceEntry & { value: number; hadRecentInput: boolean })[]) {
          if (!entry.hadRecentInput)
            Reflect.set(globalThis, 'atriumShift', Number(Reflect.get(globalThis, 'atriumShift')) + entry.value);
        }
      }).observe({ type: 'layout-shift', buffered: true });
    });
    await gotoHome(page);
    await expect(page.locator('.home__weather .home__skeleton')).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: 'Weather in Hillfield' })).toBeVisible();
    await page.waitForTimeout(300);
    const shift = await page.evaluate(() => Number(Reflect.get(globalThis, 'atriumShift')));
    expect(shift).toBeLessThanOrEqual(0.05);
  });
});

test.describe('home failures', () => {
  test.use({ viewport: desktop, allowStatus: [503] });

  test('a failed start keeps the text and says so', async ({ page }) => {
    await useScenario(page, { home: { startFails: true } });
    await gotoHome(page);
    const box = page.getByRole('textbox', { name: 'Message' });
    await box.fill('Plan the weekend');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByText('The assistant is busy. Try again in a moment.').first()).toBeVisible();
    await expect(box).toHaveValue('Plan the weekend');
    await expect(page).toHaveURL(/:\d+\/$/);
  });

  test('a failed load offers a retry', async ({ page }) => {
    await useScenario(page, { home: { fail: true } });
    await gotoHome(page);
    await expect(page.getByRole('heading', { name: 'Home did not load' })).toBeVisible({ timeout: 15_000 });
    await page.evaluate(() => localStorage.setItem('atrium.mock', '{}'));
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('heading', { level: 2, name: 'Weather in Hillfield' })).toBeVisible();
  });
});

test.describe('home on a phone', () => {
  test.use(phone);

  test('weather, then Needs you, then a 50 px composer docked above the tab bar', async ({ page }) => {
    await useScenario(page, { needsYou: 12, home: { digest: 7 } });
    await gotoHome(page);
    await expect(page.getByRole('heading', { level: 2, name: 'Weather in Hillfield' })).toBeVisible();
    const weather = await page.locator('.home__weather').boundingBox();
    const needs = await needsYou(page).boundingBox();
    expect(weather && needs && weather.y < needs.y).toBe(true);
    await expect(page.locator('.home__digest')).toBeHidden();
    const dock = await page.locator('.home__composer').boundingBox();
    const tabbar = await page.locator('.tabbar').boundingBox();
    if (!dock || !tabbar) throw new Error('no dock or tab bar');
    expect(Math.abs(dock.y + dock.height - tabbar.y)).toBeLessThanOrEqual(1);
    const composer = await page.locator('.home__composer .composer').boundingBox();
    expect(Math.round(composer?.height ?? 0)).toBe(50);
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    const last = await needsYou(page).boundingBox();
    const docked = await page.locator('.home__composer').boundingBox();
    if (!last || !docked) throw new Error('no list or dock');
    expect(last.y + last.height).toBeLessThanOrEqual(docked.y + 1);
    await expect(page.getByRole('button', { name: 'Send' })).toHaveCSS('width', '44px');
  });

  test('compose from the docked composer opens a chat', async ({ page }) => {
    await gotoHome(page);
    await page.getByRole('textbox', { name: 'Message' }).fill('Remind me about the bins');
    await page.getByRole('button', { name: 'Send' }).tap();
    await expect(page).toHaveURL(/\/chat\/\d+$/);
  });
});

test.describe('home design checks', () => {
  for (const layout of homeLayouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      test('passes the design checks and axe', async ({ page }) => {
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await gotoHome(page);
        if (layout.width > 860) await expect(page.locator('.home__digest .digest__list > li')).toHaveCount(5);
        else await expect(page.getByRole('heading', { level: 2, name: 'Weather in Hillfield' })).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, ['.rail', '.tabbar']);
        await expectNoBlue(page);
        if (layout.touch) await expectTouchTargets(page);
        await expectFocusVisible(page, '.home');
        await expectReducedMotion(page);
      });
    });
  }

  test('the empty, off and failed states pass axe', async ({ page }) => {
    await page.setViewportSize(desktop);
    for (const scenario of [
      { needsYou: 0, home: { digest: 0, weather: 'none' as const } },
      { modules: { assistant: 'off' as const } },
    ]) {
      await useScenario(page, scenario);
      await gotoHome(page);
      await expect(needsYou(page).getByRole('heading', { level: 2 })).toBeVisible();
      await expectNoSeriousAxeFindings(page);
    }
  });
});
