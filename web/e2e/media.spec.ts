import type { Page } from '@playwright/test';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, reconnect, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];

function torrent(page: Page, hash: string) {
  return page.locator(`li[data-torrent="${hash}"]`);
}

function toast(page: Page, text: string) {
  return page.locator('.toast', { hasText: text });
}

function writes(page: Page): string[] {
  const sent: string[] = [];
  page.on('request', (request) => {
    if (request.method() !== 'GET') sent.push(new URL(request.url()).pathname);
  });
  return sent;
}

async function holdDown(page: Page, name: string): Promise<void> {
  const button = page.getByRole('button', { name });
  await button.scrollIntoViewIfNeeded();
  const box = await button.boundingBox();
  if (!box) throw new Error(`${name} has no box`);
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(1300);
  await page.mouse.up();
}

test.describe('Downloads', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('lists every torrent with its state, size and who added it', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await expect(page.getByRole('heading', { level: 1, name: 'Downloads' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Downloads' }).getByRole('heading', { level: 2 })).toHaveCount(5);
    await expect(torrent(page, 'a1')).toContainText('Downloading');
    await expect(torrent(page, 'a1')).toContainText('42%');
    await expect(torrent(page, 'a1')).toContainText('Added by atrium');
    await expect(torrent(page, 'c3')).toContainText('Added by atrium');
    await expect(torrent(page, 'e5')).toContainText('Not added by atrium');
    await expect(torrent(page, 'e5')).toContainText('Size unknown');
    await expect(torrent(page, 'd4')).toContainText('Error');
    await expect(page.getByText('5 torrents: 1 active, 2 stopped, 2 done')).toBeVisible();
  });

  test('the Library hub links open these screens', async ({ page }) => {
    await gotoShell(page, '/apps');
    const library = page.locator('[data-app="media"]');
    await expect(library.getByRole('link', { name: /^Downloads/ })).toHaveAttribute('href', '/apps/torrents');
    await expect(library.getByRole('link', { name: /^Channels/ })).toHaveAttribute('href', '/apps/media/following');
    await expect(library.getByRole('link', { name: /^Library/ })).toHaveAttribute('href', '/apps/media');
  });

  test('pause and resume each offer Undo', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'a1')
      .getByRole('button', { name: /^Pause/ })
      .click();
    await expect(torrent(page, 'a1')).toContainText('Paused');
    await toast(page, 'Paused Harbour.Lights').getByRole('button', { name: 'Undo' }).click();
    await expect(toast(page, 'Resumed Harbour.Lights.S02E07.1080p.WEB again.')).toBeVisible();
    await expect(torrent(page, 'a1').getByRole('button', { name: /^Pause/ })).toBeVisible();
    await torrent(page, 'b2')
      .getByRole('button', { name: /^Resume/ })
      .click();
    await expect(torrent(page, 'b2')).toContainText('Downloading');
    await toast(page, 'Resumed The.Quiet').getByRole('button', { name: 'Undo' }).click();
    await expect(torrent(page, 'b2').getByRole('button', { name: /^Resume/ })).toBeVisible();
  });

  test('remove without files asks first and keeps the files', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'c3')
      .getByRole('button', { name: /^Remove/ })
      .click();
    const dialog = page.getByRole('dialog', { name: 'Remove Lantern.Street.S01E03.720p?' });
    await expect(dialog.getByText('Its files stay on disk.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Remove', exact: true }).click();
    await expect(toast(page, 'Removed Lantern.Street.S01E03.720p. Its files stay on disk.')).toBeVisible();
    await expect(torrent(page, 'c3')).toHaveCount(0);
  });

  test('remove with files sends at once on desktop', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'd4')
      .getByRole('button', { name: /^Remove/ })
      .click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Also delete the downloaded files').check();
    await expect(dialog.getByText('This cannot be undone.')).toBeVisible();
    await expectRoundedSurfaces(page, edges);
    await expectNoHorizontalScroll(page);
    await expectNoSeriousAxeFindings(page);
    await dialog.getByRole('button', { name: 'Remove and delete files' }).click();
    await expect.poll(() => sent, { timeout: 2_000 }).toEqual(['/web/v1/media/torrents/d4/control']);
    await expect(toast(page, 'Removed Paper.Boats.1998.Remastered and deleted its files.')).toBeVisible();
    await expect(torrent(page, 'd4')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Undo' })).toHaveCount(0);
  });

  test('keyboard alone pauses and removes', async ({ page, browserName }) => {
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'a1')
      .getByRole('button', { name: /^Pause/ })
      .focus();
    await page.keyboard.press('Enter');
    await expect(torrent(page, 'a1')).toContainText('Paused');
    await torrent(page, 'c3')
      .getByRole('button', { name: /^Remove/ })
      .focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await torrent(page, 'c3')
      .getByRole('button', { name: /^Remove/ })
      .focus();
    await page.keyboard.press('Enter');
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press(browserName === 'webkit' ? 'Alt+Tab' : 'Tab');
    await expect(dialog.getByRole('button', { name: 'Remove', exact: true })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(torrent(page, 'c3')).toHaveCount(0);
  });

  test.describe('refusals', () => {
    test.use({ allowStatus: [409], allowConsole: unreachableConsole });

    test('a refusal shows the server reason and changes nothing', async ({ page }) => {
      await useScenario(page, {
        media: { refuse: { code: 'refused', message: 'qBittorrent is protecting this torrent.' } },
      });
      await gotoShell(page, '/apps/torrents');
      await torrent(page, 'e5')
        .getByRole('button', { name: /^Pause/ })
        .click();
      await expect(torrent(page, 'e5').getByText('atrium refused this')).toBeVisible();
      await expect(
        torrent(page, 'e5').getByText('qBittorrent is protecting this torrent. Nothing was changed.'),
      ).toBeVisible();
      await expect(torrent(page, 'e5')).toContainText('Seeding');
    });

    test('an unknown outcome says so and is not sent again after a reconnect', async ({ page }) => {
      await useScenario(page, {
        media: { refuse: { code: 'effect_unknown', message: 'qBittorrent did not confirm the change.' } },
      });
      const sent = writes(page);
      await gotoShell(page, '/apps/torrents');
      await torrent(page, 'e5')
        .getByRole('button', { name: /^Pause/ })
        .click();
      await expect(torrent(page, 'e5').getByText('The outcome is unknown')).toBeVisible();
      await expect(
        torrent(page, 'e5').getByText(
          'qBittorrent did not confirm the change. atrium reloaded the list and did not try again.',
        ),
      ).toBeVisible();
      expect(sent).toEqual(['/web/v1/media/torrents/e5/control']);

      await reconnect(page, /\/web\/v1\/media\/torrents$/);
      expect(sent).toEqual(['/web/v1/media/torrents/e5/control']);
    });
  });

  test('an empty client and an unreachable client each say so', async ({ page }) => {
    await useScenario(page, { media: { torrents: 'unavailable' } });
    await gotoShell(page, '/apps/torrents');
    await expect(page.getByText('qBittorrent is not answering')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No downloads' })).toBeVisible();
  });

  test.describe('errors', () => {
    test.use({ allowStatus: [503] });

    test('a failed read offers Retry', async ({ page }) => {
      await useScenario(page, { media: { torrents: 'error' } });
      await gotoShell(page, '/apps/torrents');
      await expect(page.getByRole('heading', { name: 'Downloads did not load' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    });
  });

  test('more than one page of torrents all load', async ({ page }) => {
    await useScenario(page, { media: { torrents: 'many' } });
    await gotoShell(page, '/apps/torrents');
    await expect(page.getByText(/^145 torrents:/)).toBeVisible();
    await expect(page.getByRole('list', { name: 'Downloads' }).getByRole('listitem')).toHaveCount(145);
  });
});

test.describe('Downloads on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('remove with files needs a press and hold', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await torrent(page, 'd4')
      .getByRole('button', { name: /^Remove/ })
      .click();
    const sheet = page.locator('dialog.sheet');
    await sheet.getByText('Also delete the downloaded files').click();
    await expect(sheet.getByRole('button', { name: 'Hold to remove and delete files' })).toBeVisible();
    await sheet.getByRole('button', { name: 'Hold to remove and delete files' }).click();
    await expect(torrent(page, 'd4')).toBeVisible();
    await holdDown(page, 'Hold to remove and delete files');
    await expect(toast(page, 'Removed Paper.Boats.1998.Remastered and deleted its files.')).toBeVisible();
    await expect(torrent(page, 'd4')).toHaveCount(0);
  });
});

test.describe('Library', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the catalogue lists titles, searches and opens a title with a link', async ({ page }) => {
    await gotoShell(page, '/apps/media');
    await expect(page.getByRole('heading', { level: 1, name: 'Library' })).toBeVisible();
    await expect(page.getByText('The library server refuses the saved key.')).toBeVisible();
    const catalogue = page.getByRole('list', { name: 'Catalogue' });
    await expect(catalogue.getByRole('link')).toHaveCount(5);
    await page.getByLabel('Search the library').fill('harbour');
    await page.getByRole('button', { name: 'Search' }).click();
    await expect(catalogue.getByRole('link')).toHaveCount(1);
    await catalogue.getByRole('link', { name: /Harbour Lights/ }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour Lights' })).toBeVisible();
    await expect(page.getByText(/^S01E02 · /)).toBeVisible();
    await page.getByRole('button', { name: 'Get link for Harbour Lights S01E01.mkv' }).click();
    await expect(page.getByRole('link', { name: 'Open the link' })).toHaveAttribute('href', /f-s1-1/);
    await expect(page.getByText('Works for 4 hours')).toBeVisible();
  });

  test('follow and unfollow each offer Undo', async ({ page }) => {
    await gotoShell(page, '/apps/media/following');
    const list = page.getByRole('list', { name: 'Followed shows' });
    await expect(list.getByRole('listitem')).toHaveCount(2);
    await page.getByLabel('Show name').fill('Cedar Workshop');
    await page.getByRole('button', { name: 'Follow', exact: true }).click();
    await expect(list.getByText('Cedar Workshop')).toBeVisible();
    await toast(page, 'Following Cedar Workshop.').getByRole('button', { name: 'Undo' }).click();
    await expect(list.getByText('Cedar Workshop')).toHaveCount(0);
    await list.getByRole('button', { name: 'Unfollow Lantern Street' }).click();
    await expect(list.getByText('Lantern Street')).toHaveCount(0);
    await toast(page, 'Stopped following Lantern Street.').getByRole('button', { name: 'Undo' }).click();
    await expect(list.getByText('Lantern Street')).toBeVisible();
    await expect(list.getByText('Pattern Lantern.Street')).toBeVisible();
  });

  test('an empty Following list says what to do', async ({ page }) => {
    await useScenario(page, { media: { following: 'empty' } });
    await gotoShell(page, '/apps/media/following');
    await expect(page.getByRole('heading', { name: 'Not following anything' })).toBeVisible();
  });

  test('intake previews, files and ignores', async ({ page }) => {
    await gotoShell(page, '/apps/media/intake');
    const list = page.getByRole('list', { name: 'Waiting to be filed' });
    await expect(list.getByRole('heading', { level: 2 })).toHaveCount(2);
    const first = list.getByRole('listitem').filter({ hasText: 'Lantern.Street.S01E03.720p' }).first();
    await first.getByRole('button', { name: 'Show plan' }).click();
    await expect(first.getByText('Show: Lantern Street (2025)')).toBeVisible();
    await expect(first.getByText('S01E03 · to Shows/Lantern Street (2025)/Season 01')).toBeVisible();
    await first.getByRole('button', { name: 'File Lantern.Street.S01E03.720p' }).click();
    await expect(toast(page, 'Filed Lantern Street: 1 file placed.')).toBeVisible();
    await expect(list.getByRole('heading', { level: 2 })).toHaveCount(1);
    await list.getByRole('button', { name: 'Ignore Paper.Boats.Extras' }).click();
    await expect(toast(page, 'Ignored Paper.Boats.Extras.')).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Nothing to file' })).toBeVisible();
  });

  test.describe('with an unknown outcome', () => {
    test.use({ allowStatus: [409], allowConsole: unreachableConsole });

    test('filing says so and is not sent again after a reconnect', async ({ page }) => {
      await useScenario(page, {
        media: { refuse: { code: 'effect_unknown', message: 'The move did not confirm.' } },
      });
      const sent = writes(page);
      await gotoShell(page, '/apps/media/intake');
      const list = page.getByRole('list', { name: 'Waiting to be filed' });
      await list.getByRole('button', { name: 'Ignore Paper.Boats.Extras' }).click();
      const item = list.getByRole('listitem').filter({ hasText: 'Paper.Boats.Extras' }).first();
      await expect(item.getByText('The outcome is unknown')).toBeVisible();
      await expect(
        item.getByText('The move did not confirm. atrium reloaded the list and did not try again.'),
      ).toBeVisible();
      expect(sent).toEqual([expect.stringMatching(/^\/web\/v1\/media\/filings\/[^/]+\/ignore$/)]);

      await reconnect(page, /\/web\/v1\/media\/queue$/);
      expect(sent).toEqual([expect.stringMatching(/^\/web\/v1\/media\/filings\/[^/]+\/ignore$/)]);
    });
  });

  test.describe('with Torrents off', () => {
    test.use({ allowStatus: [404] });

    test('Library is unavailable, names the reason and links to the directory', async ({ page }) => {
      await useScenario(page, { apps: { off: ['torrents'] } });
      for (const path of ['/apps/media', '/apps/media/following', '/apps/media/intake']) {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { name: 'Library is unavailable' })).toBeVisible();
        await expect(page.getByText('Torrents is off (needs torrents).')).toBeVisible();
        await expect(page.getByRole('link', { name: 'Open the apps directory' })).toHaveAttribute(
          'href',
          '/apps/directory',
        );
      }
      await gotoShell(page, '/apps/torrents');
      await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
    });
  });

  test('Library off is not found', async ({ page }) => {
    await useScenario(page, { apps: { off: ['media'] } });
    await gotoShell(page, '/apps/media');
    await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
  });
});

test.describe('media offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('every write is disabled and nothing is sent', async ({ page }) => {
    await gotoShell(page, '/apps/torrents');
    await expect(torrent(page, 'a1')).toBeVisible();
    await goUnreachable(page);
    const pause = torrent(page, 'a1').getByRole('button', { name: /^Pause/ });
    await expect(pause).toHaveAttribute('aria-disabled', 'true');
    await expect(torrent(page, 'a1').getByRole('button', { name: /^Remove/ })).toHaveAttribute('aria-disabled', 'true');
    await pause.click({ force: true });
    await expect(torrent(page, 'a1')).toContainText('Downloading');
    await expect(toast(page, 'Paused')).toHaveCount(0);
  });
});

test.describe('media layouts', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of [
        '/apps/torrents',
        '/apps/media',
        '/apps/media/following',
        '/apps/media/intake',
        '/apps/media/item/show/s1',
      ]) {
        test(`${path} fits, rounds and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(page.locator('.card, .lib-tile, .lib-files__row, .lib-follows__row').first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});
