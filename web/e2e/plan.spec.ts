import type { Page } from '@playwright/test';
import type { InboxRequest } from '../src/mocks/inbox.ts';
import {
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, galleryLayouts, test, unreachableConsole, useScenario } from './fixtures.ts';
import { expectLostAnswer } from './lost.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet'];

function requests(page: Page): Promise<InboxRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumInboxRequests') ?? []);
}

function toasts(page: Page) {
  return page.getByRole('list', { name: 'Notifications' });
}

test.describe('plan on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('To do groups by due date, with sources, links and counts', async ({ page }) => {
    await gotoShell(page, '/plan');
    await expect(page.getByRole('heading', { level: 1, name: 'Plan' })).toBeVisible();
    await expect(page.getByText('4 things you owe, 1 overdue')).toBeVisible();
    const nav = page.getByRole('navigation', { name: 'Plan' });
    await expect(nav.getByRole('link', { name: 'To do 4' })).toHaveAttribute('aria-current', 'page');
    await expect(nav.getByRole('link', { name: 'Waiting 1' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Overdue' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Today' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No date' })).toBeVisible();
    const row = page.getByRole('link', { name: /Send Sam the bed measurements/ });
    await expect(row).toContainText('From mail');
    await row.click();
    await expect(page).toHaveURL(/\/plan\/item\/commitment(:|%3A)8$/);
    const detail = page.getByRole('article');
    await expect(detail).toContainText('Mail: Garden plans');
    await expect(detail).toContainText('Owed to');
    await expect(detail.getByRole('button', { name: 'Done' })).toBeVisible();
    await expect(detail.getByRole('link', { name: 'Edit in Tasks' })).toHaveCount(0);
  });

  test('Waiting shows who owes you', async ({ page }) => {
    await gotoShell(page, '/plan/waiting');
    await expect(page.getByText('1 thing owed to you')).toBeVisible();
    await expect(page.getByRole('link', { name: /Bring the drill and two spades/ })).toContainText(
      'Sam Okafor owes you',
    );
    await page.getByRole('radio', { name: 'Done 1' }).check();
    await expect(page.getByRole('link', { name: /Share the seed sheet/ })).toBeVisible();
  });

  test('done, dismiss and reopen each come with an Undo that reverses them', async ({ page }) => {
    await gotoShell(page, '/plan');
    await page.getByRole('button', { name: 'Done: Renew the library card' }).click();
    await expect(toasts(page)).toContainText('Marked done: Renew the library card');
    await expect(page.getByRole('link', { name: /Renew the library card/ })).toHaveCount(0);
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(page.getByRole('link', { name: /Renew the library card/ })).toBeVisible();

    await page.getByRole('link', { name: /Book the bike service/ }).click();
    await page.getByRole('article').getByRole('button', { name: 'Dismiss' }).click();
    await expect(toasts(page)).toContainText('Dismissed: Book the bike service');
    await expect(page.getByRole('article').getByRole('button', { name: 'Reopen' })).toBeVisible();
    await page.getByRole('article').getByRole('button', { name: 'Reopen' }).click();
    await expect(toasts(page)).toContainText('Reopened: Book the bike service');
    await expect(page.getByRole('article').getByRole('button', { name: 'Dismiss' })).toBeVisible();

    const sent = await requests(page);
    expect(sent.map((r) => `${r.method} ${r.path}`)).toEqual([
      'POST /web/v1/commitments/task:31/done',
      'POST /web/v1/undo/write:900',
      'POST /web/v1/commitments/task:32/dismiss',
      'POST /web/v1/commitments/task:32/reopen',
    ]);
    expect(new Set(sent.map((r) => r.key)).size).toBe(sent.length);
  });

  test('search filters the list and Load more pages through the cursor', async ({ page }) => {
    await useScenario(page, { plan: { many: 60 } });
    await gotoShell(page, '/plan');
    await expect(page.getByRole('button', { name: 'Load more' })).toBeVisible();
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.getByRole('link', { name: /Sort photo album 60/ })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Load more' })).toHaveCount(0);
    await page.getByRole('searchbox', { name: 'Search' }).fill('ladder');
    await expect(page.getByRole('link', { name: /Return the borrowed ladder/ })).toBeVisible();
    await expect(page.getByRole('link', { name: /Sort photo album/ })).toHaveCount(0);
    await page.getByRole('searchbox', { name: 'Search' }).fill('zzz');
    await expect(page.getByRole('heading', { name: 'Nothing matches' })).toBeVisible();
  });

  test('an off mail app hides mail commitments', async ({ page }) => {
    await useScenario(page, { modules: { mail: 'off' } });
    await gotoShell(page, '/plan');
    await expect(page.getByText('3 things you owe, 1 overdue')).toBeVisible();
    await expect(page.getByText('From mail')).toHaveCount(0);
  });

  test('an empty plan says so', async ({ page }) => {
    await useScenario(page, { plan: { empty: true } });
    await gotoShell(page, '/plan');
    await expect(page.getByRole('heading', { name: 'Nothing open' })).toBeVisible();
    await expect(page.getByText('Nothing to do.')).toBeVisible();
  });

  test('loading holds the space with a skeleton', async ({ page }) => {
    await useScenario(page, { plan: { delayMs: 1500 } });
    await gotoShell(page, '/plan');
    await expect(page.getByText('Loading your plan')).toBeVisible();
    await expect(page.getByRole('link', { name: /Renew the library card/ })).toBeVisible();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [404, 409, 500] });

    test('a stale item reloads and says so', async ({ page }) => {
      await useScenario(page, { plan: { conflict: true } });
      await gotoShell(page, '/plan');
      await page.getByRole('button', { name: 'Done: Renew the library card' }).click();
      await expect(page.getByText('This changed since it loaded')).toBeVisible();
      await expect(page.getByRole('link', { name: /Renew the library card/ })).toBeVisible();
    });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { plan: { fail: true } });
      await gotoShell(page, '/plan');
      await expect(page.getByRole('heading', { name: 'Your plan did not load' })).toBeVisible({ timeout: 15_000 });
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    });

    test('a deep link to a missing item says so', async ({ page }) => {
      await gotoShell(page, '/plan/item/task:999');
      await expect(page.getByRole('heading', { name: 'Nothing here' })).toBeVisible();
    });
  });

  test('a deep link opens the item beside the list', async ({ page }) => {
    await gotoShell(page, '/plan/item/task:31');
    const detail = page.getByRole('article');
    await expect(detail).toContainText('Renew the library card');
    await expect(detail.getByRole('link', { name: 'Chat: Weekend errands' })).toHaveAttribute('href', /\/chat\/4$/);
    await expect(detail.getByRole('link', { name: 'Edit in Tasks' })).toHaveAttribute('href', /\/plan\/tasks\/31$/);
  });
});

test.describe('plan with a lost answer', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  const detail = (page: Page) => page.getByRole('article');
  const commands = [
    {
      name: 'a done',
      method: 'POST',
      path: '/commitments/task:31/done',
      open: '/plan',
      press: (page: Page) => page.getByRole('button', { name: 'Done: Renew the library card' }).click(),
    },
    {
      name: 'a dismiss',
      method: 'POST',
      path: '/commitments/task:32/dismiss',
      open: '/plan/item/task:32',
      press: (page: Page) => detail(page).getByRole('button', { name: 'Dismiss' }).click(),
    },
    {
      name: 'a reopen',
      method: 'POST',
      path: '/commitments/task:32/reopen',
      open: '/plan/item/task:32',
      press: async (page: Page) => {
        await detail(page).getByRole('button', { name: 'Dismiss' }).click();
        await detail(page).getByRole('button', { name: 'Reopen' }).click();
      },
    },
    {
      name: 'an undo',
      method: 'POST',
      path: '/undo/[^/]+',
      open: '/plan',
      press: async (page: Page) => {
        await page.getByRole('button', { name: 'Done: Renew the library card' }).click();
        await toasts(page).getByRole('button', { name: 'Undo' }).click();
      },
    },
  ];

  for (const { name, ...command } of commands) {
    test(`${name} whose answer is lost reads as unknown and is never sent again`, async ({ page }) => {
      await expectLostAnswer(page, command);
    });
  }
});

test.describe('plan on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('a row opens the item alone, with a way back', async ({ page }) => {
    await gotoShell(page, '/plan');
    await page.getByRole('link', { name: /Renew the library card/ }).click();
    await expect(page.getByRole('article')).toContainText('Renew the library card');
    await expect(page.getByRole('link', { name: /Book the bike service/ })).toHaveCount(0);
    await page.getByRole('link', { name: 'All to do' }).click();
    await expect(page.getByRole('link', { name: /Book the bike service/ })).toBeVisible();
  });
});

test.describe('plan design checks', () => {
  for (const layout of galleryLayouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of ['/plan', '/plan/waiting', '/plan/item/task:31']) {
        test(`${path} fits, rounds, stays off blue and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(
            page.getByText('Renew the library card').or(page.getByText('Bring the drill')).first(),
          ).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoBlue(page);
          await expectNoSeriousAxeFindings(page);
        });
      }

      test('the empty state passes axe', async ({ page }) => {
        await useScenario(page, { plan: { empty: true } });
        await gotoShell(page, '/plan');
        await expect(page.getByRole('heading', { name: 'Nothing open' })).toBeVisible();
        await expectRoundedSurfaces(page, edges);
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});
