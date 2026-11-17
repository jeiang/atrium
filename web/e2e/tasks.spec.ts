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

test.describe('tasks on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the list shows counts, filters and links', async ({ page }) => {
    await gotoShell(page, '/plan/tasks');
    await expect(page.getByRole('heading', { level: 1, name: 'Tasks' })).toBeVisible();
    await expect(page.getByText('3 tasks open, 1 overdue')).toBeVisible();
    await expect(page.getByRole('link', { name: /Book the bike service/ })).toContainText('Space');
    await page.getByRole('radio', { name: 'Done 1' }).check();
    await expect(page.getByRole('link', { name: /Order seeds for spring/ })).toBeVisible();
    await page.getByRole('radio', { name: 'Dismissed 1' }).check();
    await expect(page.getByRole('link', { name: /Water the seedlings/ })).toBeVisible();
  });

  test('add a task linked to a chat in three presses, then Undo removes it', async ({ page }) => {
    await gotoShell(page, '/plan/tasks');
    await page.getByRole('textbox', { name: 'New task' }).fill('Fix the gate latch');
    await page.getByText('Due date, priority and link').click();
    await page.getByRole('combobox', { name: 'Link to' }).selectOption('thread');
    await page.getByRole('textbox', { name: 'Number' }).fill('4');
    await page.getByRole('button', { name: 'Add task' }).click();
    await expect(toasts(page)).toContainText('Added: Fix the gate latch');
    await expect(page.getByRole('link', { name: /Fix the gate latch/ })).toContainText('Chat');
    await expect(page.getByRole('textbox', { name: 'New task' })).toHaveValue('');
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(page.getByRole('link', { name: /Fix the gate latch/ })).toHaveCount(0);
  });

  test('edit saves with the read revision and Undo restores it', async ({ page }) => {
    await gotoShell(page, '/plan/tasks/32');
    const detail = page.getByRole('article');
    await detail.getByRole('button', { name: 'Edit' }).click();
    await detail.getByRole('textbox', { name: 'Title' }).fill('Book the bike service for Friday');
    await detail.getByRole('combobox', { name: 'Priority' }).selectOption('high');
    await detail.getByRole('button', { name: 'Save task' }).click();
    await expect(toasts(page)).toContainText('Saved: Book the bike service for Friday');
    await expect(detail.getByRole('heading', { name: 'Book the bike service for Friday' })).toBeVisible();
    await expect(detail).toContainText('high');
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(detail.getByRole('heading', { name: 'Book the bike service', exact: true })).toBeVisible();
  });

  test('complete, dismiss, reopen and delete each offer Undo', async ({ page }) => {
    await gotoShell(page, '/plan/tasks/33');
    const detail = page.getByRole('article');
    await detail.getByRole('button', { name: 'Done' }).click();
    await expect(toasts(page)).toContainText('Marked done: Return the borrowed ladder');
    await detail.getByRole('button', { name: 'Reopen' }).click();
    await expect(toasts(page)).toContainText('Reopened: Return the borrowed ladder');
    await detail.getByRole('button', { name: 'Dismiss' }).click();
    await expect(toasts(page)).toContainText('Dismissed: Return the borrowed ladder');
    await toasts(page).getByRole('button', { name: 'Undo' }).last().click();
    await expect(detail.getByRole('button', { name: 'Dismiss' })).toBeVisible();
    await detail.getByRole('button', { name: 'Delete' }).click();
    await expect(page).toHaveURL(/\/plan\/tasks$/);
    await expect(page.getByRole('link', { name: /Return the borrowed ladder/ })).toHaveCount(0);
    await toasts(page).getByRole('button', { name: 'Undo' }).last().click();
    await expect(page.getByRole('link', { name: /Return the borrowed ladder/ })).toBeVisible();
    const sent = await requests(page);
    expect(sent.map((r) => `${r.method} ${r.path}`)).toEqual([
      'POST /web/v1/tasks/33/done',
      'POST /web/v1/tasks/33/reopen',
      'POST /web/v1/tasks/33/dismiss',
      'POST /web/v1/undo/write:902',
      'DELETE /web/v1/tasks/33',
      'POST /web/v1/undo/write:903',
    ]);
    expect(new Set(sent.map((r) => r.key)).size).toBe(sent.length);
  });

  test('a mail commitment offers no edit or delete', async ({ page }) => {
    await gotoShell(page, '/plan/item/commitment:8');
    const detail = page.getByRole('article');
    await expect(detail).toContainText('From mail');
    await expect(detail.getByRole('button', { name: 'Edit' })).toHaveCount(0);
    await expect(detail.getByRole('button', { name: 'Delete' })).toHaveCount(0);
    await expect(detail.getByRole('link', { name: 'Edit in Tasks' })).toHaveCount(0);
  });

  test('an empty task list says so', async ({ page }) => {
    await useScenario(page, { plan: { empty: true } });
    await gotoShell(page, '/plan/tasks');
    await expect(page.getByRole('heading', { name: 'No open tasks' })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'New task' })).toBeVisible();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [404, 409, 500] });

    test('a stale edit reloads and says so', async ({ page }) => {
      await gotoShell(page, '/plan/tasks/31');
      const detail = page.getByRole('article');
      await detail.getByRole('button', { name: 'Edit' }).click();
      await page.evaluate(async () => {
        await fetch('/web/v1/tasks/31/done', { method: 'POST', headers: { 'X-CSRF-Token': 'mock-csrf-token' } });
      });
      await detail.getByRole('textbox', { name: 'Title' }).fill('Renew the card');
      await detail.getByRole('button', { name: 'Save task' }).click();
      await expect(page.getByText('This changed since it loaded')).toBeVisible();
      await expect(detail.getByRole('button', { name: 'Reopen' })).toBeVisible();
    });

    test('a verb on an already closed task says it changed', async ({ page }) => {
      await useScenario(page, { plan: { conflict: true } });
      await gotoShell(page, '/plan/tasks');
      await page.getByRole('button', { name: 'Done: Renew the library card' }).click();
      await expect(page.getByText('This changed since it loaded')).toBeVisible();
    });

    test('an off assistant says tasks are off', async ({ page }) => {
      await useScenario(page, { modules: { assistant: 'off' } });
      await gotoShell(page, '/plan/tasks');
      await expect(page.getByRole('heading', { name: 'Tasks are off' })).toBeVisible();
      await expect(page.getByRole('textbox', { name: 'New task' })).toHaveCount(0);
    });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { plan: { fail: true } });
      await gotoShell(page, '/plan/tasks');
      await expect(page.getByRole('heading', { name: 'Your tasks did not load' })).toBeVisible({ timeout: 15_000 });
    });

    for (const path of ['/plan/tasks/999', '/plan/tasks/abc']) {
      test(`${path} says nothing is there`, async ({ page }) => {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { name: 'Nothing here' })).toBeVisible();
      });
    }
  });
});

test.describe('tasks with a lost answer', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  const detail = (page: Page) => page.getByRole('article');
  const commands = [
    {
      name: 'an add',
      method: 'POST',
      path: '/tasks',
      open: '/plan/tasks',
      press: async (page: Page) => {
        await page.getByRole('textbox', { name: 'New task' }).fill('Fix the gate latch');
        await page.getByRole('button', { name: 'Add task' }).click();
      },
    },
    {
      name: 'an edit',
      method: 'PATCH',
      path: '/tasks/32',
      open: '/plan/tasks/32',
      press: async (page: Page) => {
        await detail(page).getByRole('button', { name: 'Edit' }).click();
        await detail(page).getByRole('textbox', { name: 'Title' }).fill('Book the bike service for Friday');
        await detail(page).getByRole('button', { name: 'Save task' }).click();
      },
    },
    {
      name: 'a done',
      method: 'POST',
      path: '/tasks/33/done',
      open: '/plan/tasks/33',
      press: (page: Page) => detail(page).getByRole('button', { name: 'Done' }).click(),
    },
    {
      name: 'a dismiss',
      method: 'POST',
      path: '/tasks/33/dismiss',
      open: '/plan/tasks/33',
      press: (page: Page) => detail(page).getByRole('button', { name: 'Dismiss' }).click(),
    },
    {
      name: 'a reopen',
      method: 'POST',
      path: '/tasks/33/reopen',
      open: '/plan/tasks/33',
      press: async (page: Page) => {
        await detail(page).getByRole('button', { name: 'Done' }).click();
        await detail(page).getByRole('button', { name: 'Reopen' }).click();
      },
    },
    {
      name: 'a delete',
      method: 'DELETE',
      path: '/tasks/33',
      open: '/plan/tasks/33',
      press: (page: Page) => detail(page).getByRole('button', { name: 'Delete' }).click(),
    },
  ];

  for (const { name, ...command } of commands) {
    test(`${name} whose answer is lost reads as unknown and is never sent again`, async ({ page }) => {
      await expectLostAnswer(page, command);
    });
  }
});

test.describe('tasks on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('quick add is the first field and adds in two presses', async ({ page }) => {
    await gotoShell(page, '/plan/tasks');
    const field = page.getByRole('textbox', { name: 'New task' });
    await field.fill('Buy twine');
    await field.press('Enter');
    await expect(toasts(page)).toContainText('Added: Buy twine');
    await expect(page.getByRole('link', { name: /Buy twine/ })).toBeVisible();
  });

  test('a deep link shows the task alone with a way back', async ({ page }) => {
    await gotoShell(page, '/plan/tasks/31');
    await expect(page.getByRole('article')).toContainText('Renew the library card');
    await page.getByRole('link', { name: 'All tasks' }).click();
    await expect(page.getByRole('textbox', { name: 'New task' })).toBeVisible();
  });
});

test.describe('tasks design checks', () => {
  for (const layout of galleryLayouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of ['/plan/tasks', '/plan/tasks/31']) {
        test(`${path} fits, rounds, stays off blue and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(page.getByText('Renew the library card').first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoBlue(page);
          await expectNoSeriousAxeFindings(page);
        });
      }

      test('the edit form passes axe', async ({ page }) => {
        await gotoShell(page, '/plan/tasks/31');
        await page.getByRole('article').getByRole('button', { name: 'Edit' }).click();
        await expect(page.getByRole('textbox', { name: 'Title' })).toBeFocused();
        await expectNoHorizontalScroll(page);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});
