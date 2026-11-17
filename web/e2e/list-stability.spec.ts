import type { Page } from '@playwright/test';
import { gotoShell } from './checks.ts';
import { expect, layouts, test, useScenario } from './fixtures.ts';

const phone = layouts[0] ?? { name: 'phone', width: 390, height: 844, touch: true };

async function settledBottom(page: Page, selector: string): Promise<{ bottom: number; height: number }> {
  return page.evaluate(async (selector) => {
    const target = [...document.querySelectorAll(selector)].at(-1);
    if (!target) throw new Error(`no ${selector}`);
    target.scrollIntoView({ block: 'end' });
    await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)));
    return { bottom: target.getBoundingClientRect().bottom, height: innerHeight };
  }, selector);
}

const lists = [
  {
    name: 'Messages',
    route: '/apps/mail',
    item: '.mail-item-wrap',
    count: 30,
    scenario: { mailbox: { messages: 'many' } },
  },
  {
    name: 'Packages',
    route: '/apps/packages',
    item: '.pk-list > li',
    count: 136,
    scenario: { apps: { on: ['packages'] }, packages: { list: 'many' } },
  },
  {
    name: 'Downloads',
    route: '/apps/torrents',
    item: '.dl-list > li',
    count: 145,
    scenario: { media: { torrents: 'many' } },
  },
] as const;

test.describe('Long lists stay put under the finger', () => {
  test.use({ viewport: { width: phone.width, height: phone.height }, hasTouch: true });

  for (const list of lists) {
    test(`${list.name}: the last item stays on screen once scrolled to`, async ({ page }) => {
      await useScenario(page, list.scenario as never);
      await gotoShell(page, list.route);
      await expect(page.locator(list.item)).toHaveCount(list.count);
      const { bottom, height } = await settledBottom(page, list.item);
      expect(bottom).toBeLessThanOrEqual(height + 1);
      expect(bottom).toBeGreaterThan(0);
    });
  }

  test('Messages: Show more stays on screen and loads the next page when tapped', async ({ page }) => {
    await useScenario(page, { mailbox: { messages: 'many' } });
    await gotoShell(page, '/apps/mail');
    await expect(page.locator('.mail-item-wrap')).toHaveCount(30);
    const more = page.getByRole('button', { name: 'Show more' });
    const { bottom, height } = await settledBottom(page, '.mail-list + button');
    expect(bottom).toBeLessThanOrEqual(height + 1);
    await more.tap();
    await expect.poll(() => page.locator('.mail-item-wrap').count()).toBeGreaterThan(30);
  });
});

test.describe('The Inbox stays put when the queue arrives', () => {
  const desktop = layouts[2] ?? { name: 'desktop', width: 1440, height: 900, touch: false };
  for (const layout of [phone, desktop]) {
    test(`at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await useScenario(page, { inbox: { readDelay: 1000 } });
      const list = page.locator(layout.touch ? '.deck' : '.inbox');
      await page.goto('/inbox');
      await expect(list.locator('.skel-group').first()).toBeVisible();
      const before = await list.evaluate((el) => el.getBoundingClientRect().top);
      await expect(page.locator('.page-lede')).toContainText('waiting on you');
      expect(await list.evaluate((el) => el.getBoundingClientRect().top)).toBe(before);
    });
  }
});

test.describe('Later Inbox pages do not move what is on screen', () => {
  const desktop = layouts[2] ?? { name: 'desktop', width: 1440, height: 900, touch: false };
  for (const layout of [phone, desktop]) {
    test(`at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await useScenario(page, { inbox: { pageSize: 2, pageDelay: 600 } });
      const list = page.locator(layout.touch ? '.deck' : '.inbox__list');
      const first = layout.touch ? list.locator('[data-item]').first() : list.locator('tr[data-key]').first();
      await page.goto('/inbox');
      await expect(first).toBeVisible();
      await expect(list).toHaveAttribute('aria-busy', 'true');
      const lede = await page.locator('.page-lede').textContent();
      const top = await first.evaluate((el) => el.getBoundingClientRect().top);
      if (!layout.touch) await expect(list.locator('tr[data-key]')).toHaveCount(2);
      await expect(list).not.toHaveAttribute('aria-busy', 'true', { timeout: 10_000 });
      if (!layout.touch) expect(await list.locator('tr[data-key]').count()).toBeGreaterThan(2);
      expect(await first.evaluate((el) => el.getBoundingClientRect().top)).toBe(top);
      await expect(page.locator('.page-lede')).toHaveText(lede ?? '');
    });
  }
});
