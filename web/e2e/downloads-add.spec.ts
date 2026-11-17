import type { Page } from '@playwright/test';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';
import { expectLostAnswer } from './lost.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const hash = '0123456789abcdef0123456789abcdef01234567';
const magnet = `magnet:?xt=urn:btih:${hash.toUpperCase()}&dn=Night.Ferry.S01E01.1080p`;
const torrentFile = {
  name: 'Cedar.Lights.torrent',
  mimeType: 'application/x-bittorrent',
  buffer: Buffer.from('d4:infod4:name12:Cedar.Lightsee'),
};

interface Sent {
  method: string;
  path: string;
  body: unknown;
}

function writes(page: Page): Sent[] {
  const sent: Sent[] = [];
  page.on('request', (request) => {
    if (request.method() === 'GET') return;
    const path = new URL(request.url()).pathname;
    const type = request.headers()['content-type'] ?? '';
    sent.push({
      method: request.method(),
      path,
      body: type.includes('json') ? request.postDataJSON() : type.split(';')[0],
    });
  });
  return sent;
}

function toast(page: Page, text: string) {
  return page.locator('.toasts .toast', { hasText: text });
}

function torrent(page: Page, id: string) {
  return page.locator(`li[data-torrent="${id}"]`);
}

async function settled(page: Page): Promise<void> {
  await files(page).evaluate((element) =>
    Promise.all(
      element
        .getAnimations({ subtree: true })
        .filter((animation) => animation.effect?.getComputedTiming().endTime !== Infinity)
        .map((animation) => animation.finished),
    ),
  );
}

function addPanel(page: Page) {
  return page.getByRole('region', { name: 'Add a torrent' });
}

function files(page: Page) {
  return page.locator('dialog[open]');
}

test.describe('Add a torrent', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('a magnet link is added with one press and its files are offered', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/torrents');
    await expect(torrent(page, 'a1')).toBeVisible();
    await addPanel(page).getByLabel('Magnet link').fill(magnet);
    await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
    await expect(toast(page, 'Added Night.Ferry.S01E01.1080p.')).toBeVisible();
    await expect(torrent(page, hash)).toContainText('Added by atrium');
    await expect(addPanel(page).getByLabel('Magnet link')).toHaveValue('');
    expect(sent).toEqual([{ method: 'POST', path: '/web/v1/media/torrents', body: { magnet } }]);

    await toast(page, 'Added Night.Ferry').getByRole('button', { name: 'Choose files' }).click();
    await expect(files(page).getByRole('heading', { name: 'Files in Night.Ferry.S01E01.1080p' })).toBeVisible();
    await expect(files(page).getByRole('heading', { name: 'No file list yet' })).toBeVisible();
    await expect(files(page).getByRole('button', { name: 'Apply' })).toHaveCount(0);
  });

  test.describe('refused input', () => {
    test.use({ allowStatus: [422] });

    test('an empty or wrong magnet link says why at the field', async ({ page }) => {
      const sent = writes(page);
      await gotoShell(page, '/apps/torrents');
      const field = addPanel(page).getByLabel('Magnet link');
      await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
      await expect(addPanel(page).getByText('Paste a magnet link or choose a .torrent file.')).toBeVisible();
      await expect(field).toHaveAttribute('aria-invalid', 'true');
      await expect(field).toBeFocused();
      expect(sent).toEqual([]);

      await field.fill('https://example.invalid/file.torrent');
      await expect(field).not.toHaveAttribute('aria-invalid', 'true');
      await field.press('Enter');
      await expect(addPanel(page).getByText('That is not a magnet link.')).toBeVisible();
      await expect(field).toHaveAttribute('aria-invalid', 'true');
      await field.fill('magnet:?dn=Nothing');
      await field.press('Enter');
      await expect(addPanel(page).getByText('The magnet link has no v1 info hash (xt=urn:btih:...).')).toBeVisible();
      expect(sent.map((item) => item.path)).toEqual(['/web/v1/media/torrents', '/web/v1/media/torrents']);
      await expect(page.getByRole('list', { name: 'Downloads' }).getByRole('heading', { level: 2 })).toHaveCount(5);
    });

    test('a file that is not a torrent is refused and nothing is added', async ({ page }) => {
      await gotoShell(page, '/apps/torrents');
      await page
        .locator('input[name="torrent"]')
        .setInputFiles({ name: 'notes.torrent', mimeType: 'text/plain', buffer: Buffer.from('plain words') });
      await expect(addPanel(page).getByText('atrium did not add it')).toBeVisible();
      await expect(addPanel(page).getByText('not a torrent file Nothing was added.')).toBeVisible();
      await expect(page.getByRole('list', { name: 'Downloads' }).getByRole('heading', { level: 2 })).toHaveCount(5);
    });
  });

  test('a .torrent file is uploaded, then Apply sends only the changed files', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/torrents');
    await page.locator('input[name="torrent"]').setInputFiles(torrentFile);
    await expect(toast(page, 'Added Cedar.Lights.')).toBeVisible();
    expect(sent).toEqual([{ method: 'POST', path: '/web/v1/media/torrents', body: 'multipart/form-data' }]);

    await toast(page, 'Added Cedar.Lights.').getByRole('button', { name: 'Choose files' }).click();
    const dialog = files(page);
    const nfo = dialog.getByRole('checkbox', { name: /Cedar\.Lights\.nfo/ });
    await expect(dialog.getByRole('checkbox')).toHaveCount(2);
    await expect(nfo).toBeChecked();
    await expect(dialog.getByRole('button', { name: 'Apply' })).toBeDisabled();
    await nfo.uncheck();
    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(toast(page, 'Saved file choices for Cedar.Lights.')).toBeVisible();
    await expect(dialog).toHaveCount(0);
    expect(sent.slice(1)).toEqual([
      {
        method: 'PUT',
        path: expect.stringMatching(/\/web\/v1\/media\/torrents\/[0-9a-f]{40}\/files$/),
        body: { index: 1, wanted: false },
      },
    ]);

    const added = page.locator('li[data-torrent]', { hasText: 'Cedar.Lights' });
    await added.getByRole('button', { name: 'Files in Cedar.Lights' }).click();
    await expect(files(page).getByRole('checkbox', { name: /Cedar\.Lights\.nfo/ })).not.toBeChecked();
  });
});

test.describe('Choose files', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('Select all and Apply send one request per changed file', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'a1').getByRole('button', { name: 'Files in Harbour.Lights.S02E07.1080p.WEB' }).click();
    const dialog = files(page);
    await expect(dialog.getByRole('checkbox')).toHaveCount(3);
    await expect(dialog.getByRole('checkbox', { name: /sample\.mkv/ })).not.toBeChecked();
    await expect(dialog.getByText(/42% downloaded/)).toBeVisible();
    await dialog.getByRole('button', { name: 'Select all' }).click();
    await dialog.getByRole('button', { name: 'Apply' }).click();
    await expect(toast(page, 'Saved file choices for Harbour.Lights.S02E07.1080p.WEB.')).toBeVisible();
    expect(sent).toEqual([
      { method: 'PUT', path: '/web/v1/media/torrents/a1/files', body: { index: 2, wanted: true } },
    ]);
  });

  test('Cancel sends nothing and forgets the changes', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/torrents');
    const open = torrent(page, 'b2').getByRole('button', { name: 'Files in The.Quiet.Orchard.2024.1080p' });
    await open.click();
    await files(page).getByRole('checkbox', { name: /\.nfo/ }).uncheck();
    await files(page).getByRole('button', { name: 'Cancel' }).click();
    await expect(files(page)).toHaveCount(0);
    await open.click();
    await expect(files(page).getByRole('checkbox', { name: /\.nfo/ })).toBeChecked();
    expect(sent).toEqual([]);
  });

  test('a torrent atrium did not add shows its files read only', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'e5').getByRole('button', { name: 'Files in Garden.Club.Lectures.Vol2' }).click();
    const dialog = files(page);
    await expect(dialog.getByText('Read only')).toBeVisible();
    await expect(dialog.getByRole('checkbox').first()).toBeDisabled();
    await expect(dialog.getByRole('button', { name: 'Apply' })).toHaveCount(0);
    await expect(dialog.getByRole('button', { name: 'Close' })).toBeVisible();
  });

  test.describe('refused', () => {
    test.use({ allowStatus: [409] });

    test('a refusal keeps the dialog open with the server reason', async ({ page }) => {
      await useScenario(page, {
        media: { refuse: { code: 'refused', message: 'qBittorrent is protecting this torrent.' } },
      });
      await gotoShell(page, '/apps/torrents');
      await torrent(page, 'a1')
        .getByRole('button', { name: /^Files in/ })
        .click();
      const dialog = files(page);
      await dialog.getByRole('checkbox', { name: /\.srt/ }).uncheck();
      await dialog.getByRole('button', { name: 'Apply' }).click();
      await expect(dialog.getByText('atrium refused this')).toBeVisible();
      await expect(dialog.getByText('qBittorrent is protecting this torrent. Nothing was changed.')).toBeVisible();
      await expect(dialog.getByRole('checkbox', { name: /\.srt/ })).toBeChecked();
    });
  });
});

test.describe('Add and choose while offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('Add and Apply send nothing', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await expect(torrent(page, 'a1')).toBeVisible();
    await torrent(page, 'a1')
      .getByRole('button', { name: /^Files in/ })
      .click();
    await expect(files(page).getByRole('checkbox')).toHaveCount(3);
    await files(page).getByRole('button', { name: 'Cancel' }).click();
    await goUnreachable(page);
    const sent = writes(page);
    await expect(addPanel(page).getByRole('button', { name: 'Add', exact: true })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    await expect(addPanel(page).getByRole('button', { name: 'Add a .torrent file' })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    const field = addPanel(page).getByLabel('Magnet link');
    await field.fill(magnet);
    await field.press('Enter');

    await torrent(page, 'a1')
      .getByRole('button', { name: /^Files in/ })
      .click();
    await files(page).getByRole('checkbox', { name: /\.srt/ }).uncheck();
    await expect(files(page).getByRole('button', { name: 'Apply' })).toHaveAttribute('aria-disabled', 'true');
    expect(sent).toEqual([]);
  });
});

test.describe('add and files layouts', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({
        viewport: { width: layout.width, height: layout.height },
        hasTouch: layout.touch,
        allowStatus: [422],
      });

      test('the add panel with a refused link fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/torrents');
        await expect(torrent(page, 'a1')).toBeVisible();
        const field = addPanel(page).getByLabel('Magnet link');
        await field.fill('not a link');
        await field.press('Enter');
        await expect(addPanel(page).getByText('That is not a magnet link.')).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });

      test('the files dialog fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/torrents');
        await torrent(page, 'a1')
          .getByRole('button', { name: /^Files in/ })
          .click();
        await expect(files(page).getByRole('checkbox')).toHaveCount(3);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        await settled(page);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoSeriousAxeFindings(page);
      });

      test('a read-only file list fits and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/torrents');
        await torrent(page, 'e5')
          .getByRole('button', { name: /^Files in/ })
          .click();
        await expect(files(page).getByText('Read only')).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        await settled(page);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});

test.describe('torrents with a lost answer', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('an add whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/media/torrents',
      open: '/apps/torrents',
      press: async (page) => {
        await addPanel(page).getByLabel('Magnet link').fill(magnet);
        await addPanel(page).getByRole('button', { name: 'Add', exact: true }).click();
      },
    });
  });

  test('a file choice whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'PUT',
      path: '/media/torrents/a1/files',
      open: '/apps/torrents',
      press: async (page) => {
        await torrent(page, 'a1').getByRole('button', { name: 'Files in Harbour.Lights.S02E07.1080p.WEB' }).click();
        await files(page).getByRole('button', { name: 'Select all' }).click();
        await files(page).getByRole('button', { name: 'Apply' }).click();
      },
    });
  });
});
