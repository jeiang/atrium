import type { Page } from '@playwright/test';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goReachable, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

const edgeSurfaces = ['.rail', '.tabbar', 'dialog.sheet'];
const desktop = layouts[2] ?? { name: 'desktop', width: 1440, height: 900, touch: false };

async function open(page: Page, path: string): Promise<void> {
  await useScenario(page, { chat: { speed: 20 } });
  await gotoShell(page, path);
}

function transcript(page: Page) {
  return page.getByRole('list', { name: 'Messages' });
}

function toasts(page: Page) {
  return page.locator('.toasts');
}

function chatList(page: Page) {
  return page.getByRole('list', { name: 'Chats' });
}

async function settled(page: Page): Promise<void> {
  await expect(page.getByRole('button', { name: 'Stop run' })).toHaveCount(0, { timeout: 20_000 });
  await expect(page.locator('.transcript .status-slot')).toHaveCount(0);
}

async function chatAction(page: Page, name: string): Promise<void> {
  await page.getByRole('button', { name: 'Chat actions' }).click();
  await page.getByRole('menuitem', { name }).click();
}

async function rowAction(page: Page, title: string, name: string): Promise<void> {
  await page.getByRole('button', { name: `Actions for ${title}` }).click();
  await page.getByRole('menuitem', { name }).click();
}

function writes(page: Page): string[] {
  const seen: string[] = [];
  page.on('request', (request) => {
    if (request.method() !== 'GET') seen.push(`${request.method()} ${new URL(request.url()).pathname}`);
  });
  return seen;
}

test.describe('chat list actions', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('pins a chat to the top, and Undo unpins it', async ({ page }) => {
    await open(page, '/chat');
    const row = chatList(page).getByRole('listitem').filter({ hasText: 'Winter tyre sizes' });
    await expect(row).not.toContainText('Pinned');
    await rowAction(page, 'Winter tyre sizes', 'Pin to the top');
    await expect(toasts(page).getByText('Pinned “Winter tyre sizes”.')).toBeVisible();
    await expect(chatList(page).getByRole('listitem').first()).toContainText('Winter tyre sizes');
    await expect(chatList(page).getByRole('listitem').first()).toContainText('Pinned');
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(toasts(page).getByText('Unpinned “Winter tyre sizes”.')).toBeVisible();
    await expect(row).not.toContainText('Pinned');
    await expect(chatList(page).getByRole('listitem').first()).not.toContainText('Winter tyre sizes');
  });

  test('archives a chat out of the list and restores it from Archived', async ({ page }) => {
    await open(page, '/chat');
    await rowAction(page, 'Winter tyre sizes', 'Archive');
    await expect(toasts(page).getByText('Archived “Winter tyre sizes”.')).toBeVisible();
    await expect(chatList(page).getByRole('link', { name: /Winter tyre sizes/ })).toHaveCount(0);
    await page.getByRole('radio', { name: 'Archived' }).check();
    const archived = page.getByRole('list', { name: 'Archived chats' });
    await expect(archived.getByRole('listitem')).toHaveCount(1);
    await rowAction(page, 'Winter tyre sizes', 'Restore from archive');
    await expect(toasts(page).getByText('Restored “Winter tyre sizes”.')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No archived chats' })).toBeVisible();
    await page.getByRole('radio', { name: 'Chats' }).check();
    await expect(chatList(page).getByRole('link', { name: /Winter tyre sizes/ })).toBeVisible();
  });
});

test.describe('chat header actions', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('pins and archives from the header, and the list follows', async ({ page }) => {
    await open(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await chatAction(page, 'Pin to the top');
    await expect(toasts(page).getByText('Pinned “Winter tyre sizes”.')).toBeVisible();
    await page.getByRole('button', { name: 'Chat actions' }).click();
    await expect(page.getByRole('menuitem', { name: 'Unpin' })).toBeVisible();
    await page.getByRole('menuitem', { name: 'Archive' }).click();
    await expect(toasts(page).getByText('Archived “Winter tyre sizes”.')).toBeVisible();
    await expect(page.locator('.chat__origin')).toContainText('Archived');
    await page.getByRole('link', { name: 'All chats' }).click();
    await expect(chatList(page).getByRole('link', { name: /Winter tyre sizes/ })).toHaveCount(0);
  });

  test('moves a chat to a space and the list shows the space', async ({ page }) => {
    await open(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await chatAction(page, 'Move to a space');
    const dialog = page.getByRole('dialog', { name: 'Move to a space' });
    await expect(dialog.getByRole('radio', { name: 'No space' })).toBeChecked();
    await expect(dialog.getByRole('button', { name: 'Move' })).toBeDisabled();
    await dialog.getByRole('radio', { name: 'Shared garden' }).check();
    await dialog.getByRole('button', { name: 'Move' }).click();
    await expect(dialog).toBeHidden();
    await expect(toasts(page).getByText('Moved “Winter tyre sizes” to Shared garden.')).toBeVisible();
    await page.getByRole('link', { name: 'All chats' }).click();
    await expect(chatList(page).getByRole('listitem').filter({ hasText: 'Winter tyre sizes' })).toContainText(
      'Shared garden',
    );
  });

  test('deletes a chat after a confirm that opens on Cancel', async ({ page }) => {
    await open(page, '/chat/11');
    await expect(transcript(page).getByText('two roof sheets', { exact: false })).toBeVisible();
    await chatAction(page, 'Delete chat');
    const dialog = page.getByRole('dialog', { name: 'Delete this chat?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Chat actions' })).toBeFocused();
    await chatAction(page, 'Delete chat');
    await dialog.getByRole('button', { name: 'Delete chat' }).click();
    await expect(page).toHaveURL(/\/chat$/);
    await expect(toasts(page).getByText('Deleted “Check the shed roof”.')).toBeVisible();
    await expect(chatList(page).getByRole('link', { name: /Check the shed roof/ })).toHaveCount(0);
  });

  test.describe('refusals', () => {
    test.use({ allowStatus: [409] });

    test('a running chat is not deleted and the dialog says why', async ({ page }) => {
      await useScenario(page, { chat: { speed: 0.2 } });
      await gotoShell(page, '/chat/7');
      await expect(page.getByRole('button', { name: 'Stop run' })).toBeVisible();
      await chatAction(page, 'Delete chat');
      const dialog = page.getByRole('dialog', { name: 'Delete this chat?' });
      await expect(dialog).toContainText('A run is in progress.');
      const refused = page.waitForResponse(
        (response) => response.request().method() === 'DELETE' && response.status() === 409,
      );
      await dialog.getByRole('button', { name: 'Delete chat' }).click();
      await refused;
      await expect(dialog.getByText('Stop the run, then delete the chat.', { exact: false })).toBeVisible();
      await expect(page).toHaveURL(/\/chat\/7$/);
    });
  });
});

test.describe('the last turn', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('Regenerate starts a new run that replaces the last reply', async ({ page }) => {
    await open(page, '/chat/11');
    await expect(transcript(page).getByText('two roof sheets', { exact: false })).toBeVisible();
    const started = page.waitForResponse(
      (response) => /\/threads\/11\/regenerate$/.test(new URL(response.url()).pathname) && response.status() === 202,
    );
    await page.getByRole('button', { name: 'Regenerate (new run)' }).click();
    await started;
    await expect(transcript(page).getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(transcript(page).getByText('two roof sheets', { exact: false })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Regenerate (new run)' })).toBeVisible();
  });

  test('Edit sends the corrected last message as a new run', async ({ page }) => {
    await open(page, '/chat/11');
    await expect(transcript(page).getByText('two roof sheets', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    const field = page.getByRole('textbox', { name: 'Edit your last message' });
    await expect(field).toHaveValue('Does the shed roof need new sheets?');
    await field.fill('Does the shed roof need four new sheets?');
    await page.getByRole('button', { name: 'Send edit (new run)' }).click();
    await expect(field).toHaveCount(0);
    await expect(transcript(page).getByText('Does the shed roof need four new sheets?')).toBeVisible();
    await expect(transcript(page).getByText('Delivery takes three days')).toBeVisible({ timeout: 15_000 });
    await settled(page);
    await expect(transcript(page).getByText('Does the shed roof need new sheets?')).toHaveCount(0);
  });

  test('Branch from here opens a new chat that links back', async ({ page }) => {
    await open(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Branch from here' }).last().click();
    await expect(page).toHaveURL(/\/chat\/200$/);
    await expect(toasts(page).getByText('Branched “Winter tyre sizes” into a new chat.')).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Winter tyre sizes' })).toBeVisible();
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await page.getByRole('link', { name: 'Branched from an earlier chat' }).click();
    await expect(page).toHaveURL(/\/chat\/9$/);
  });

  test('a running chat offers no edit, regenerate or branch', async ({ page }) => {
    await useScenario(page, { chat: { speed: 0.2 } });
    await gotoShell(page, '/chat/7');
    await expect(page.getByRole('button', { name: 'Stop run' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Regenerate (new run)' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Branch from here' })).toHaveCount(0);
  });
});

test.describe('work panel', () => {
  test.use({ viewport: { width: desktop.width, height: desktop.height } });

  test('shows the latest run, the plan, sources, sub-runs, files and drafts', async ({ page }) => {
    await open(page, '/chat/11');
    await expect(transcript(page).getByText('two roof sheets', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Work', exact: true }).focus();
    await page.keyboard.press('Enter');
    const panel = page.getByRole('dialog', { name: 'Work' });
    const run = panel.getByRole('region', { name: 'Latest run' });
    await expect(run).toContainText('Finished');
    await expect(run.locator('dd').nth(1)).toHaveText('1');
    await expect(panel.getByRole('region', { name: 'Plan' })).toContainText('Price two new sheets');
    await expect(
      panel.getByRole('region', { name: 'Sources' }).getByRole('link', { name: 'Roof sheet sizes' }),
    ).toBeVisible();
    await expect(panel.getByRole('region', { name: 'Sub-runs' })).toContainText('Check the roof photos');
    await expect(panel.getByRole('region', { name: 'Files' })).toContainText('roof-photo.jpg');
    await expect(panel.getByRole('region', { name: 'Drafts' })).toContainText('Ask the builder for a quote');
    await expect(panel.getByRole('region', { name: 'Asks' })).toContainText('Nothing waits for you.');
    await expect(panel.getByRole('region', { name: 'Context' })).toContainText('Nothing is summarized yet.');
    await panel.getByRole('button', { name: 'Close' }).last().click();
    await expect(panel).toBeHidden();
    await expect(page.getByRole('button', { name: 'Work', exact: true })).toBeFocused();
  });

  test('zooms the context from a summary down to one message', async ({ page }) => {
    await open(page, '/chat/9');
    await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
    await page.getByRole('button', { name: 'Work', exact: true }).click();
    const context = page.getByRole('dialog', { name: 'Work' }).getByRole('region', { name: 'Context' });
    await expect(context.getByRole('button', { name: /^Messages \d+ to \d+/ })).toHaveCount(5);
    for (const range of ['Messages 1 to 16', 'Messages 1 to 8', 'Messages 1 to 4', 'Messages 1 to 2', 'Message 1']) {
      const toggle = context.getByRole('button', { name: new RegExp(`^${range}\\b`) });
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    }
    await expect(context.locator('.history__leaf')).toHaveText('Question 1: which tyre size fits the small car?');
  });
});

test.describe('while atrium is unreachable', () => {
  test.use({ allowConsole: unreachableConsole, viewport: { width: desktop.width, height: desktop.height } });

  test('pin from the list is refused at once and never sent later', async ({ page }) => {
    const sent = writes(page);
    await open(page, '/chat');
    await expect(chatList(page).getByRole('listitem')).toHaveCount(30);
    await goUnreachable(page);
    await rowAction(page, 'Winter tyre sizes', 'Pin to the top');
    await expect(toasts(page).getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    await expect(chatList(page).getByRole('listitem').filter({ hasText: 'Winter tyre sizes' })).not.toContainText(
      'Pinned',
    );
    await goReachable(page);
    expect(sent).toEqual([]);
  });

  test('regenerate, edit, branch and delete are refused at once and never sent later', async ({ page }) => {
    const sent = writes(page);
    await open(page, '/chat/11');
    await expect(page.getByRole('button', { name: 'Regenerate (new run)' })).toBeVisible();
    await goUnreachable(page);
    await page.getByRole('button', { name: 'Regenerate (new run)' }).click();
    await expect(toasts(page).getByText('Cannot reach atrium. No new run was started.')).toBeVisible();
    await page.getByRole('button', { name: 'Branch from here' }).click();
    await expect(toasts(page).getByText('Cannot reach atrium. No branch was made.')).toBeVisible();
    await page.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByRole('textbox', { name: 'Edit your last message' }).fill('Does the roof leak?');
    await page.getByRole('button', { name: 'Send edit (new run)' }).click();
    await expect(page.getByText('Cannot reach atrium. Nothing was sent. The edit was kept.')).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Edit your last message' })).toHaveValue('Does the roof leak?');
    await chatAction(page, 'Delete chat');
    const dialog = page.getByRole('dialog', { name: 'Delete this chat?' });
    await dialog.getByRole('button', { name: 'Delete chat' }).click();
    await expect(dialog.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await goReachable(page);
    expect(sent).toEqual([]);
    await expect(page).toHaveURL(/\/chat\/11$/);
  });
});

test.describe('design checks', () => {
  for (const layout of layouts) {
    test(`${layout.name}: list actions, header actions, dialogs, edit and the work panel pass the design and axe checks`, async ({
      page,
    }) => {
      test.setTimeout(90_000);
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await page.emulateMedia({ reducedMotion: 'reduce' });
      const check = async (surface = 'main'): Promise<void> => {
        if (layout.touch) await expectTouchTargets(page, surface);
        await expectRoundedSurfaces(page, edgeSurfaces);
        await expectNoHorizontalScroll(page);
        await expectNoSeriousAxeFindings(page);
      };
      await open(page, '/chat');
      await expect(chatList(page)).toBeVisible();
      await check();
      await page.getByRole('button', { name: 'Actions for Winter tyre sizes' }).click();
      await expect(page.getByRole('menuitem', { name: 'Pin to the top' })).toBeVisible();
      await check('[role="menu"]:popover-open');
      await page.keyboard.press('Escape');
      await page.getByRole('radio', { name: 'Archived' }).check();
      await expect(page.getByRole('heading', { name: 'No archived chats' })).toBeVisible();
      await check();

      await gotoShell(page, '/chat/11');
      await expect(page.getByRole('button', { name: 'Regenerate (new run)' })).toBeVisible();
      await check();
      await page.getByRole('button', { name: 'Edit', exact: true }).click();
      await expect(page.getByRole('textbox', { name: 'Edit your last message' })).toBeVisible();
      await check();
      await page.getByRole('button', { name: 'Cancel' }).click();

      await page.getByRole('button', { name: 'Work', exact: true }).click();
      const panel = page.getByRole('dialog', { name: 'Work' });
      await expect(panel.getByRole('region', { name: 'Files' })).toContainText('roof-photo.jpg');
      await check('dialog[open]');
      await panel.getByRole('button', { name: 'Close' }).last().click();
      await expect(panel).toBeHidden();

      await chatAction(page, 'Move to a space');
      const move = page.getByRole('dialog', { name: 'Move to a space' });
      await expect(move.getByRole('radio', { name: 'Shared garden' })).toBeVisible();
      await check('dialog[open]');
      await move.getByRole('button', { name: 'Cancel' }).click();
      await expect(move).toBeHidden();

      await chatAction(page, 'Delete chat');
      const confirm = page.getByRole('dialog', { name: 'Delete this chat?' });
      await expect(confirm.getByRole('button', { name: 'Cancel' })).toBeFocused();
      await check('dialog[open]');
      await confirm.getByRole('button', { name: 'Cancel' }).click();

      await gotoShell(page, '/chat/9');
      await expect(transcript(page).getByText('Answer 40:', { exact: false })).toBeVisible();
      await page.getByRole('button', { name: 'Work', exact: true }).click();
      const context = page.getByRole('dialog', { name: 'Work' }).getByRole('region', { name: 'Context' });
      await context.getByRole('button', { name: /^Messages 1 to 16\b/ }).click();
      await expect(context.getByRole('button', { name: /^Messages 1 to 8\b/ })).toBeVisible();
      await check('dialog[open]');
    });
  }
});
