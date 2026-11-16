import type { Page } from '@playwright/test';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, layouts, test, useScenario } from './fixtures.ts';

const desktop = { width: 1440, height: 900 };
const phone = { viewport: { width: 390, height: 844 }, hasTouch: true };
const edgeSurfaces = ['.rail', '.tabbar'];

async function streamsOpened(page: Page): Promise<number> {
  return page.evaluate(() => Number(Reflect.get(globalThis, 'atriumMockStreams') ?? 0));
}

async function frameBoxes(page: Page): Promise<Record<string, number[]>> {
  return page.evaluate(() => {
    const out: Record<string, number[]> = {};
    const selectors = ['.rail .brand', '.rail-link', '.rail__foot', '.topbar', '.topbar .brand', '.tabbar a', '.main'];
    for (const selector of selectors) {
      const boxes = [...document.querySelectorAll(selector)]
        .filter((el) => !el.closest('.rail__sub'))
        .map((el) => el.getBoundingClientRect());
      boxes.forEach((box, index) => {
        if (box.width === 0 && box.height === 0) return;
        out[`${selector}#${index}`] = [box.x, box.y, box.width, box.height];
      });
    }
    return out;
  });
}

function shiftScore(before: Record<string, number[]>, after: Record<string, number[]>, width: number, height: number) {
  let area = 0;
  let distance = 0;
  for (const [key, [x0 = 0, y0 = 0, w0 = 0, h0 = 0]] of Object.entries(before)) {
    const [x1 = 0, y1 = 0, w1 = 0, h1 = 0] = after[key] ?? [];
    const moved = Math.max(Math.abs(x1 - x0), Math.abs(y1 - y0));
    if (moved <= 1) continue;
    area += w0 * h0 + w1 * h1;
    distance = Math.max(distance, moved);
  }
  return Math.min(1, area / (width * height)) * (distance / Math.max(width, height));
}

test.describe('signed in', () => {
  test.use({ viewport: desktop });

  test('the rail shows the five destinations, the inbox count and the identity', async ({ page }) => {
    await gotoShell(page, '/');
    const rail = page.getByRole('navigation', { name: 'Main' });
    for (const name of ['Home', 'Plan', 'Knowledge', 'Apps', 'Assistant']) {
      await expect(rail.getByRole('link', { name, exact: true })).toBeVisible();
    }
    await expect(rail.getByRole('link', { name: 'Home', exact: true })).toHaveAttribute('aria-current', 'page');
    await expect(rail.getByRole('link', { name: /^Inbox\W+3 waiting$/ })).toBeVisible();
    await expect(rail.getByRole('link', { name: 'Mail' })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Tabs' })).toBeHidden();
    await page.getByRole('button', { name: 'Account: Operator' }).click();
    const menu = page.getByRole('menu', { name: 'Account' });
    await expect(menu.getByRole('menuitem', { name: 'Chats' })).toBeFocused();
    await expect(menu.getByRole('menuitem', { name: 'Settings' })).toBeVisible();
    await expect(menu.getByRole('menuitem', { name: 'Sign out' })).toBeVisible();
    await page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('menuitem', { name: 'Settings' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
    await expect(page.getByRole('button', { name: 'Account: Operator' })).toBeFocused();
  });

  test('back and forward move between destinations', async ({ page }) => {
    await gotoShell(page, '/');
    const rail = page.getByRole('navigation', { name: 'Main' });
    await rail.getByRole('link', { name: 'Knowledge', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
    await rail.getByRole('link', { name: 'Mail' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Mail' })).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/knowledge$/);
    await expect(rail.getByRole('link', { name: 'Knowledge', exact: true })).toHaveAttribute('aria-current', 'page');
    await page.goBack();
    await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeVisible();
    await page.goForward();
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
  });

  test('the skip link moves focus to the main region', async ({ page, browserName }) => {
    await gotoShell(page, '/');
    await page.keyboard.press(browserName === 'webkit' && process.platform === 'darwin' ? 'Alt+Tab' : 'Tab');
    const skip = page.getByRole('link', { name: 'Skip to content' });
    await expect(skip).toBeFocused();
    await expect(skip).toBeInViewport();
    await page.keyboard.press('Enter');
    await expect(page.locator('main')).toBeFocused();
  });

  test('a module that is off hides its destination and its apps', async ({ page }) => {
    await useScenario(page, { modules: { assistant: 'off' }, apps: { off: ['media'] } });
    await gotoShell(page, '/');
    const rail = page.getByRole('navigation', { name: 'Main' });
    await expect(rail.getByRole('link', { name: 'Mail' })).toBeVisible();
    for (const name of ['Plan', 'Knowledge', 'Assistant', 'Media']) {
      await expect(rail.getByRole('link', { name, exact: true })).toHaveCount(0);
    }
    await gotoShell(page, '/apps/media');
    await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
  });

  test('a paused mailbox shows on the Mail row', async ({ page }) => {
    await useScenario(page, { mail: { mode: 'live', paused: true } });
    await gotoShell(page, '/');
    await expect(page.getByRole('link', { name: /^Mail/ })).toContainText('Paused');
  });

  test('an app event refreshes the inbox count', async ({ page }) => {
    await gotoShell(page, '/');
    const rail = page.getByRole('navigation', { name: 'Main' });
    await expect(rail.getByRole('link', { name: /^Inbox\W+3 waiting$/ })).toBeVisible();
    await expect.poll(() => streamsOpened(page)).toBe(1);
    await page.evaluate(() => {
      localStorage.setItem('atrium.mock', JSON.stringify({ needsYou: 5 }));
      const channel = new BroadcastChannel('atrium-mock-events');
      channel.postMessage({ topic: 'review' });
      channel.close();
    });
    await expect(rail.getByRole('link', { name: /^Inbox\W+5 waiting$/ })).toBeVisible();
  });

  test('two tabs share one event stream and hand it over', async ({ context }) => {
    const first = await context.newPage();
    await gotoShell(first, '/');
    await expect.poll(() => streamsOpened(first)).toBe(1);
    const second = await context.newPage();
    await gotoShell(second, '/inbox');
    await second.waitForTimeout(500);
    expect(await streamsOpened(second)).toBe(0);
    const locks = await second.evaluate(async () => {
      const state = await navigator.locks.query();
      return {
        held: state.held?.filter((lock) => lock.name === 'atrium-app-events').length,
        pending: state.pending?.filter((lock) => lock.name === 'atrium-app-events').length,
      };
    });
    expect(locks).toEqual({ held: 1, pending: 1 });
    await first.close();
    await expect.poll(() => streamsOpened(second)).toBe(1);
    await second.evaluate(() => {
      localStorage.setItem('atrium.mock', JSON.stringify({ needsYou: 7 }));
      const channel = new BroadcastChannel('atrium-mock-events');
      channel.postMessage({ topic: 'review' });
      channel.close();
    });
    await expect(
      second.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /^Inbox\W+7 waiting$/ }),
    ).toBeVisible();
  });

  test('without a sign-out route the menu offers no sign out', async ({ page }) => {
    await useScenario(page, { signOut: false });
    await gotoShell(page, '/');
    await page.getByRole('button', { name: 'Account: Operator' }).click();
    await expect(page.getByRole('menuitem', { name: 'Settings' })).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Sign out' })).toHaveCount(0);
  });
});

test.describe('signed out', () => {
  test.use({ viewport: desktop, allowStatus: [401, 503] });

  test('sign out sends the CSRF token and ends signed out', async ({ page }) => {
    await gotoShell(page, '/');
    await page.getByRole('button', { name: 'Account: Operator' }).click();
    await page.getByRole('menuitem', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'You are signed out' })).toBeVisible();
    await expect(page).toHaveTitle('You are signed out · atrium');
    await expect(page.getByRole('link', { name: 'Sign in' })).toHaveAttribute(
      'href',
      /\/login\?reason=expired&next=%2F$/,
    );
    await expect(page.getByRole('button', { name: /^Account/ })).toHaveCount(0);
  });

  test('with a sign-in page the client goes there with a return path', async ({ page }) => {
    await useScenario(page, { session: 'signed-out' });
    await page.goto('/plan?view=week');
    await expect(page).toHaveURL(/\/login\?reason=expired&next=%2Fplan%3Fview%3Dweek$/);
  });

  test('without a sign-in page the client explains what to do', async ({ page }) => {
    await useScenario(page, { session: 'signed-out-proxy' });
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in to atrium' })).toBeVisible();
    await expect(page.getByText('atrium takes your identity from the proxy in front of it.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Sign in' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    await expectNoSeriousAxeFindings(page);
  });

  test('a 401 during use goes to sign-in once', async ({ page }) => {
    await useScenario(page, { expireOn: 'shell' });
    await page.goto('/knowledge');
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in to atrium' })).toBeVisible();
    expect(new URL(page.url()).search).toBe('?reason=expired&next=%2Fknowledge');
    await page.goto('/knowledge');
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in to atrium' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Sign in' })).toBeVisible();
    await expect(page).toHaveURL(/\/knowledge$/);
  });

  test('an unavailable sign-in shows an error with retry', async ({ page }) => {
    await useScenario(page, { session: 'unavailable' });
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Sign-in is unavailable' })).toBeVisible();
    await page.evaluate(() => localStorage.setItem('atrium.mock', '{}'));
    await page.unrouteAll();
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeVisible();
  });
});

test.describe('phone', () => {
  test.use(phone);

  test('the tab bar shows Home, Decide, Chats and Apps with the inbox count', async ({ page }) => {
    await gotoShell(page, '/');
    const tabs = page.getByRole('navigation', { name: 'Tabs' });
    for (const name of ['Home', 'Decide', 'Chats', 'Apps']) {
      await expect(tabs.getByRole('link', { name: new RegExp(`^${name}`) })).toBeVisible();
    }
    await expect(tabs.getByRole('link', { name: /^Decide\W+3 waiting$/ })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeHidden();
    await tabs.getByRole('link', { name: 'Apps' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Apps' })).toBeFocused();
    await expect(tabs.getByRole('link', { name: 'Apps' })).toHaveAttribute('aria-current', 'page');
    await page.getByRole('link', { name: 'Knowledge' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
    await expect(tabs.getByRole('link', { name: 'Apps' })).toHaveAttribute('aria-current', 'page');
    await expectTouchTargets(page);
    await page.getByRole('button', { name: 'Account: Operator' }).click();
    await expect(page.getByRole('menuitem', { name: 'Settings' })).toBeVisible();
    await expectTouchTargets(page, '.account__popup');
  });
});

test.describe('prerendered frame', () => {
  test('the raw document carries the frame without inline script or style', async ({ request }) => {
    const html = await (await request.get('/plan')).text();
    expect(html).toContain('<nav class="rail" aria-label="Main">');
    expect(html).toContain('<nav class="tabbar" aria-label="Tabs">');
    for (const label of ['Home', 'Inbox', 'Plan', 'Knowledge', 'Apps', 'Assistant', 'Decide', 'Chats']) {
      expect(html).toContain(`>${label}</span>`);
    }
    expect(html).not.toMatch(/\sstyle=/);
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)[^>]*>/);
  });

  for (const layout of layouts) {
    test(`${layout.name}: the client keeps the frame geometry`, async ({ browser }) => {
      const options = { viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch };
      const still = await browser.newContext(options);
      await still.route('**/*.js', (route) => route.abort());
      const raw = await still.newPage();
      await raw.goto('/', { waitUntil: 'load' });
      await raw.evaluate(() => document.fonts.ready);
      const before = await frameBoxes(raw);
      await still.close();
      const live = await browser.newContext(options);
      const page = await live.newPage();
      await gotoShell(page, '/');
      await expect(page.getByRole('link', { name: /3 waiting$/ }).first()).toBeVisible();
      const after = await frameBoxes(page);
      await live.close();
      expect(Object.keys(before).length).toBeGreaterThan(3);
      expect(shiftScore(before, after, layout.width, layout.height)).toBeLessThanOrEqual(0.05);
    });
  }
});

test.describe('design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      test('the open account menu fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/');
        await page.getByRole('button', { name: 'Account: Operator' }).click();
        await expect(page.getByRole('menu', { name: 'Account' })).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edgeSurfaces);
        await expectNoSeriousAxeFindings(page);
      });

      test.describe('signed out', () => {
        test.use({ allowStatus: [401] });

        test('the signed-out state fits and passes axe', async ({ page }) => {
          await useScenario(page, { session: 'signed-out-proxy' });
          await page.goto('/');
          await expect(page.getByRole('heading', { level: 1, name: 'Sign in to atrium' })).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edgeSurfaces);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      });
    });
  }
});
