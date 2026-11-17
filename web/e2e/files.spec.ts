import type { Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks';
import { expect, goUnreachable, layouts, reconnect, test, unreachableConsole, useScenario } from './fixtures';

interface Sent {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const NOTES = { name: 'shed-notes.txt', mimeType: 'text/plain', buffer: Buffer.from('Paint the door sage green.\n') };
const LIST = { name: 'tool-list.txt', mimeType: 'text/plain', buffer: Buffer.from('Saw, drill, level.\n') };

async function sent(page: Page): Promise<Sent[]> {
  return page.evaluate(
    () => (globalThis as typeof globalThis & { atriumKnowledgeRequests?: Sent[] }).atriumKnowledgeRequests ?? [],
  );
}

async function openGarden(page: Page): Promise<void> {
  await gotoShell(page, '/knowledge/spaces/5');
  await expect(page.getByRole('heading', { level: 1, name: 'Shared garden' })).toBeVisible();
  await expect(page.getByRole('list', { name: 'Files in this space' })).toBeVisible();
}

async function stage(page: Page, ...files: (typeof NOTES)[]): Promise<void> {
  await page.locator('input[type="file"]').setInputFiles(files);
  await expect(page.getByRole('list', { name: 'Files ready to upload' }).getByRole('listitem')).toHaveCount(
    files.length,
  );
}

function spaceFiles(page: Page) {
  return page.getByRole('list', { name: 'Files in this space' });
}

test.describe('files in spaces and chats', () => {
  test('a space lists its files and opens a preview', async ({ page }) => {
    await openGarden(page);
    await expect(spaceFiles(page).getByRole('listitem')).toHaveCount(2);
    await spaceFiles(page).getByRole('link', { name: /garden-seeds\.csv/ }).click();
    await expect(page).toHaveURL(/\/knowledge\/spaces\/5\/files\/602$/);
    await expect(page.getByRole('heading', { level: 1, name: 'garden-seeds.csv' })).toBeVisible();
    const table = page.getByRole('table', { name: 'garden-seeds.csv' });
    await expect(table.getByRole('columnheader')).toHaveText(['Seed', 'Sow by', 'Bed']);
    await expect(table.getByRole('row')).toHaveCount(4);
    await expect(page.getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      '/web/v1/knowledge/spaces/5/files/602/content',
    );
    await page.getByRole('link', { name: 'Back to the space' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Shared garden' })).toBeVisible();
  });

  test('picking files stages them and one keyed upload adds them', async ({ page }) => {
    await openGarden(page);
    await stage(page, NOTES, LIST);
    await page.getByRole('button', { name: 'Take tool-list.txt off the upload' }).click();
    await expect(page.getByRole('list', { name: 'Files ready to upload' }).getByRole('listitem')).toHaveCount(1);
    expect(await sent(page)).toEqual([]);
    await page.getByRole('button', { name: 'Upload 1 file' }).click();
    await expect(page.locator('.toast__text', { hasText: 'Added 1 file to Shared garden.' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Files ready to upload' })).toHaveCount(0);
    await expect(spaceFiles(page).getByRole('listitem')).toHaveCount(3);
    const writes = await sent(page);
    expect(writes.map((item) => [item.method, item.path])).toEqual([['POST', '/web/v1/knowledge/spaces/5/files']]);
    expect(writes[0]?.key).toMatch(UUID);
    await spaceFiles(page).getByRole('link', { name: /shed-notes\.txt/ }).click();
    await expect(page.getByText('Paint the door sage green.')).toBeVisible();
  });

  test('deleting a file asks first, focuses Cancel and sends one keyed delete', async ({ page }) => {
    await openGarden(page);
    await page.getByRole('button', { name: 'Delete bed-plan.md' }).focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Delete bed-plan.md?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Delete bed-plan.md' })).toBeFocused();
    expect(await sent(page)).toEqual([]);
    await page.keyboard.press('Enter');
    await dialog.getByRole('button', { name: 'Delete file' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('.toast__text', { hasText: 'Deleted bed-plan.md.' })).toBeVisible();
    await expect(spaceFiles(page).getByRole('listitem')).toHaveCount(1);
    const writes = await sent(page);
    expect(writes.map((item) => [item.method, item.path])).toEqual([
      ['DELETE', '/web/v1/knowledge/spaces/5/files/603'],
    ]);
    expect(writes[0]?.key).toMatch(UUID);
  });

  test('archiving asks first, locks the files and Restore brings the space back', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openGarden(page);
    await page.getByRole('button', { name: 'Archive' }).click();
    const dialog = page.getByRole('dialog', { name: 'Archive Shared garden?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Archive space' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('.toast__text', { hasText: 'Archived Shared garden.' })).toBeVisible();
    await expect(page.locator('.kpage__tags').getByText('Archived')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add files' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Delete / })).toHaveCount(0);
    const nav = page.getByRole('navigation', { name: 'Spaces' });
    await expect(nav.getByRole('link', { name: /Shared garden/ })).toHaveCount(0);
    await nav.getByText('Archived', { exact: true }).click();
    await expect(nav.getByRole('list', { name: 'Archived spaces' }).getByRole('link')).toHaveText([
      /Shared garden/,
      /Old allotment/,
    ]);
    await page.getByRole('button', { name: 'Restore' }).click();
    await expect(page.locator('.toast__text', { hasText: 'Restored Shared garden.' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add files' })).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => [item.method, item.path, item.body])).toEqual([
      ['POST', '/web/v1/knowledge/spaces/5/archive', { archived: true }],
      ['POST', '/web/v1/knowledge/spaces/5/archive', { archived: false }],
    ]);
    expect(writes[0]?.key).toMatch(UUID);
    expect(writes[1]?.key).toMatch(UUID);
    expect(writes[0]?.key).not.toEqual(writes[1]?.key);
  });

  test('an archived space shows its files without changing them', async ({ page }) => {
    await gotoShell(page, '/knowledge/spaces/8');
    await expect(page.getByRole('heading', { level: 1, name: 'Old allotment' })).toBeVisible();
    await expect(spaceFiles(page).getByRole('link', { name: /plot-rules\.txt/ })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Add files' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Restore' })).toBeVisible();
  });

  test('Knowledge › Files lists every chat and space file, newest first', async ({ page }) => {
    await gotoShell(page, '/knowledge/subjects');
    await page.getByRole('link', { name: /^Files/ }).click();
    await expect(page).toHaveURL(/\/knowledge\/files$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Files' })).toBeVisible();
    const rows = page.getByRole('list', { name: 'Files' }).getByRole('link');
    await expect(rows).toHaveCount(9);
    await expect(rows.first()).toContainText('order-check.log');
    await expect(rows.first()).toContainText('chat: Plan the garden shed order');
    await expect(page.getByRole('link', { name: /garden-seeds\.csv/ })).toContainText('space: Shared garden');
    await expect(page.getByRole('link', { name: /garden-seeds\.csv/ })).toHaveAttribute(
      'href',
      '/knowledge/spaces/5/files/602',
    );
    await page.getByRole('link', { name: /shed-quote\.md/ }).click();
    await expect(page).toHaveURL(/\/chat\/7\/files\/701$/);
    await expect(page.locator('.prose strong', { hasText: '5 December' })).toBeVisible();
  });

  test('chat file previews render images, refuse binaries and say when text is cut', async ({ page }) => {
    await gotoShell(page, '/chat/7/files/703');
    const image = page.getByRole('img', { name: 'shed-sketch.png' });
    await expect(image).toHaveAttribute('src', '/web/v1/threads/7/files/703/content');
    await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.naturalWidth)).toBe(96);
    await gotoShell(page, '/chat/7/files/704');
    await expect(page.getByText('No preview for this file type. Download it to open it.')).toBeVisible();
    await gotoShell(page, '/chat/7/files/705');
    await expect(page.getByText('line 1: checked the shed order')).toBeVisible();
    await expect(page.getByText('Showing the first part of the file. Download it to see the rest.')).toBeVisible();
    await page.getByRole('link', { name: 'Back to the chat' }).click();
    await expect(page).toHaveURL(/\/chat\/7$/);
  });

  test('a chat lists its files on request', async ({ page }) => {
    await gotoShell(page, '/chat/7');
    await expect(page.getByRole('heading', { level: 1, name: 'Plan the garden shed order' })).toBeVisible();
    await page.getByRole('button', { name: 'Files' }).click();
    const dialog = page.getByRole('dialog', { name: 'Files in this chat' });
    const rows = dialog.getByRole('list', { name: 'Files in this chat' }).getByRole('link');
    await expect(rows).toHaveCount(5);
    await expect(rows.nth(1)).toContainText('Generated');
    await rows.filter({ hasText: 'timber-sizes.csv' }).click();
    await expect(page).toHaveURL(/\/chat\/7\/files\/702$/);
    await expect(page.getByRole('table', { name: 'timber-sizes.csv' }).getByRole('row')).toHaveCount(4);
  });

  test('a chat without files says so', async ({ page }) => {
    await gotoShell(page, '/chat/8');
    await page.getByRole('button', { name: 'Files' }).click();
    await expect(page.getByRole('dialog', { name: 'Files in this chat' })).toContainText('No files in this chat yet.');
  });

  test('an empty library says so', async ({ page }) => {
    await useScenario(page, { knowledge: { empty: true } });
    await gotoShell(page, '/knowledge/files');
    await expect(page.getByText('No files yet')).toBeVisible();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [404, 409, 500], allowConsole: [/404|409|500/] });

    test('a missing file says so', async ({ page }) => {
      await gotoShell(page, '/knowledge/spaces/5/files/999');
      await expect(page.getByText('This file does not exist')).toBeVisible();
    });

    test('a failed library load offers a retry', async ({ page }) => {
      await useScenario(page, { knowledge: { fail: true } });
      await gotoShell(page, '/knowledge/files');
      await expect(page.getByText('Files could not be loaded')).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    });

    test('deleting a file that is already gone reloads the list', async ({ page }) => {
      await openGarden(page);
      await page.evaluate(async () => {
        await fetch('/web/v1/knowledge/spaces/5/files/603', {
          method: 'DELETE',
          headers: { 'Atrium-Client': 'web', 'Idempotency-Key': crypto.randomUUID() },
        });
      });
      await page.getByRole('button', { name: 'Delete bed-plan.md' }).click();
      await page.getByRole('dialog', { name: 'Delete bed-plan.md?' }).getByRole('button', { name: 'Delete file' }).click();
      await expect(
        page.locator('.toast__text', { hasText: 'bed-plan.md was already deleted. The list has been reloaded.' }),
      ).toBeVisible();
      await expect(spaceFiles(page).getByRole('listitem')).toHaveCount(1);
    });
  });

  test.describe('when atrium cannot be reached', () => {
    test.use({ allowConsole: unreachableConsole });

    test('writes are refused at once and nothing is sent', async ({ page }) => {
      await openGarden(page);
      await stage(page, NOTES);
      await goUnreachable(page);
      await page.getByRole('button', { name: 'Upload 1 file' }).click();
      await expect(page.locator('.kfiles__staged').getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(page.getByRole('list', { name: 'Files ready to upload' }).getByRole('listitem')).toHaveCount(1);

      await page.getByRole('button', { name: 'Delete bed-plan.md' }).click();
      const remove = page.getByRole('dialog', { name: 'Delete bed-plan.md?' });
      await remove.getByRole('button', { name: 'Delete file' }).click();
      await expect(remove.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await remove.getByRole('button', { name: 'Cancel' }).click();

      await page.getByRole('button', { name: 'Archive' }).click();
      const archive = page.getByRole('dialog', { name: 'Archive Shared garden?' });
      await archive.getByRole('button', { name: 'Archive space' }).click();
      await expect(archive.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(archive).toBeVisible();
      expect(await sent(page)).toEqual([]);
    });

    test('an upload whose answer is lost says the outcome is unknown and is not sent again', async ({ page }) => {
      await useScenario(page, { knowledge: { loseFiles: true } });
      await openGarden(page);
      await stage(page, NOTES);
      await page.getByRole('button', { name: 'Upload 1 file' }).click();
      await expect(page.getByText(/may or may not have been added\. atrium did not try again\./)).toBeVisible();
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/knowledge/spaces/5/files']);
      await reconnect(page, /\/web\/v1\/knowledge\/spaces\/5$/);
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/knowledge/spaces/5/files']);
    });

    test('a delete whose answer is lost is not sent again', async ({ page }) => {
      await useScenario(page, { knowledge: { loseFiles: true } });
      await openGarden(page);
      await page.getByRole('button', { name: 'Delete bed-plan.md' }).click();
      const dialog = page.getByRole('dialog', { name: 'Delete bed-plan.md?' });
      await dialog.getByRole('button', { name: 'Delete file' }).click();
      await expect(dialog.getByText(/Reload the list to see whether it was deleted\./)).toBeVisible();
      await reconnect(page, /\/web\/v1\/knowledge\/spaces\/5$/);
      expect((await sent(page)).map((item) => item.path)).toEqual(['/web/v1/knowledge/spaces/5/files/603']);
    });

    test('an unopened preview says so', async ({ page }) => {
      await openGarden(page);
      await goUnreachable(page);
      await spaceFiles(page).getByRole('link', { name: /garden-seeds\.csv/ }).click();
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' })).toBeVisible();
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      const check = async (scope = 'body') => {
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
        if (layout.touch) await expectTouchTargets(page, scope);
        await expectNoBlue(page);
      };
      for (const path of [
        '/knowledge/files',
        '/knowledge/spaces/5/files/602',
        '/chat/7/files/701',
        '/chat/7/files/703',
        '/chat/7/files/705',
      ]) {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
        await expect(page.getByText(/Loading/)).toHaveCount(0);
        await check();
        await expectFocusVisible(page, 'main');
      }

      await openGarden(page);
      await stage(page, NOTES, LIST);
      await check();
      await page.getByRole('button', { name: 'Delete bed-plan.md' }).click();
      await expect(page.getByRole('dialog', { name: 'Delete bed-plan.md?' })).toBeVisible();
      await check('dialog[open]');
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: 'Archive' }).click();
      await expect(page.getByRole('dialog', { name: 'Archive Shared garden?' })).toBeVisible();
      await check('dialog[open]');
      await page.keyboard.press('Escape');

      await gotoShell(page, '/knowledge/spaces/8');
      await expect(page.getByRole('heading', { level: 1, name: 'Old allotment' })).toBeVisible();
      await check();

      await gotoShell(page, '/chat/7');
      await page.getByRole('button', { name: 'Files' }).click();
      const dialog = page.getByRole('dialog', { name: 'Files in this chat' });
      await expect(dialog.getByRole('list', { name: 'Files in this chat' }).getByRole('link')).toHaveCount(5);
      await check('dialog[open]');
    });
  }
});
