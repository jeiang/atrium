import type { Page, Request } from '@playwright/test';
import {
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, reconnect, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const POLL = '/web/v1/media/channels/poll';

function toast(page: Page, text: string) {
  return page.locator('.toast', { hasText: text });
}

function writes(page: Page): Request[] {
  const sent: Request[] = [];
  page.on('request', (request) => {
    if (request.method() !== 'GET') sent.push(request);
  });
  return sent;
}

function paths(sent: Request[]): string[] {
  return sent.map((request) => new URL(request.url()).pathname);
}

function releases(page: Page) {
  return page.getByRole('region', { name: 'Recent releases' });
}

async function pickShow(page: Page, typed: string, name: string): Promise<void> {
  await page.getByRole('combobox', { name: 'Find on TMDB' }).fill(typed);
  await page.getByRole('option', { name: new RegExp(`^${name}`) }).click();
}

function tile(page: Page, title: string) {
  return page.getByRole('list', { name: 'Catalogue' }).getByRole('listitem').filter({ hasText: title });
}

async function healArt(page: Page): Promise<void> {
  await page.evaluate(() => {
    const state = JSON.parse(localStorage.getItem('atrium.mock') ?? '{}');
    localStorage.setItem('atrium.mock', JSON.stringify({ ...state, media: { ...state.media, art: {} } }));
  });
}

async function loaded(page: Page, scope: string): Promise<void> {
  await expect
    .poll(() =>
      page
        .locator(`${scope} img.lib-poster__img`)
        .evaluateAll((images) => images.every((image) => (image as HTMLImageElement).naturalWidth > 0)),
    )
    .toBe(true);
}

test.describe('Library follow helpers', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('a TMDB pick fills the name and IMDb id, previews releases and follows with them', async ({ page }) => {
    await gotoShell(page, '/apps/media/following');
    await page.getByRole('combobox', { name: 'Find on TMDB' }).fill('harb');
    await expect(page.getByRole('option')).toHaveText([/^Harbour Lights\s*2023/, /^Harbour Lights Revisited\s*2026/]);
    await page.getByRole('option', { name: /^Harbour Lights\s*2023/ }).click();
    await expect(page.getByLabel('Show name')).toHaveValue('Harbour Lights');
    await expect(page.getByLabel('IMDb id')).toHaveValue('tt0000101');
    await expect(releases(page).getByText('4 recent releases')).toBeVisible();
    await expect(releases(page).getByRole('listitem')).toHaveCount(4);

    await page.getByLabel('Release name pattern').fill('S02E08');
    await expect(releases(page).getByText('2 of 4 recent releases match')).toBeVisible();
    await expect(releases(page).getByText('Matches')).toHaveCount(2);
    await page.getByLabel('Release name pattern').fill('s02e08');
    await page.getByLabel('Match case').check();
    await expect(releases(page).getByText('0 of 4 recent releases match')).toBeVisible();

    const follow = page.waitForRequest(
      (request) => request.method() === 'POST' && new URL(request.url()).pathname === '/web/v1/media/channels',
    );
    await page.getByRole('button', { name: 'Follow', exact: true }).click();
    expect((await follow).postDataJSON()).toEqual({
      name: 'Harbour Lights',
      imdb: 'tt0000101',
      pattern: 's02e08',
      case_sensitive: true,
      tmdb_id: 4101,
    });
    await expect(toast(page, 'Following Harbour Lights.')).toBeVisible();
    await expect(page.getByLabel('IMDb id')).toHaveValue('');
    await expect(releases(page)).toHaveCount(0);
  });

  test('a typed IMDb id is never overwritten and a show without one says so', async ({ page }) => {
    await gotoShell(page, '/apps/media/following');
    await page.getByLabel('IMDb id').fill('tt0000999');
    await expect(releases(page).getByText('No recent releases for this show.')).toBeVisible();
    await pickShow(page, 'harb', 'Harbour Lights\\s*2023');
    await expect(page.getByText('TMDB lists tt0000101. The IMDb id you typed stays.')).toBeVisible();
    await expect(page.getByLabel('IMDb id')).toHaveValue('tt0000999');
    await expect(page.getByLabel('Show name')).toHaveValue('Harbour Lights');

    await page.getByLabel('Show name').fill('Harbour Lights, my cut');
    await pickShow(page, 'revisited', 'Harbour Lights Revisited');
    await expect(page.getByText('TMDB has no IMDb id for Harbour Lights Revisited.')).toBeVisible();
    await expect(page.getByLabel('Show name')).toHaveValue('Harbour Lights, my cut');
    await expect(page.getByLabel('IMDb id')).toHaveValue('tt0000999');
  });

  test.describe('with refused input', () => {
    test.use({ allowStatus: [422] });

    test('a pattern atrium cannot read is explained inline', async ({ page }) => {
      await gotoShell(page, '/apps/media/following');
      await page.getByLabel('IMDb id').fill('tt0000301');
      await expect(releases(page).getByText('3 recent releases')).toBeVisible();
      await page.getByLabel('Release name pattern').fill('S01E0(');
      await expect(releases(page).getByText('atrium cannot preview this')).toBeVisible();
      await expect(releases(page).getByText(/^pattern "S01E0\(":/)).toBeVisible();
      await page.getByLabel('Release name pattern').fill('S01E02');
      await expect(releases(page).getByText('2 of 3 recent releases match')).toBeVisible();
    });
  });

  test.describe('without a TMDB token', () => {
    test.use({ allowStatus: [503] });

    test('show search says why and the IMDb id still previews', async ({ page }) => {
      await useScenario(page, { media: { tmdb: 'off' } });
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('combobox', { name: 'Find on TMDB' }).fill('harb');
      await expect(page.getByText('Show search is unavailable')).toBeVisible();
      await expect(page.getByText('media_tmdb_token is not set')).toBeVisible();
      await page.getByLabel('IMDb id').fill('tt0000101');
      await expect(releases(page).getByText('4 recent releases')).toBeVisible();
    });

    test('a release feed failure says so', async ({ page }) => {
      await useScenario(page, { media: { releases: 'error' } });
      await gotoShell(page, '/apps/media/following');
      await page.getByLabel('IMDb id').fill('tt0000101');
      await expect(releases(page).getByText('The preview did not load')).toBeVisible();
      await expect(releases(page).getByText('The release feed did not answer.')).toBeVisible();
    });

    test('a failed check says nothing was sent', async ({ page }) => {
      await useScenario(page, { media: { poll: 'failed' } });
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('button', { name: 'Check now' }).click();
      await expect(page.getByText('The check did not run')).toBeVisible();
      await expect(page.getByText('Channel poll failed. Nothing was sent to Downloads.')).toBeVisible();
    });
  });

  test('Check now sends one keyed check per press and reports what it added', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/media/following');
    await page.getByRole('button', { name: 'Check now' }).click();
    await expect(toast(page, 'Checked followed shows. 1 new episode sent to Downloads.')).toBeVisible();
    expect(paths(sent)).toEqual([POLL]);
    await page.getByRole('button', { name: 'Check now' }).click();
    await expect(toast(page, 'Checked followed shows. Nothing new.')).toBeVisible();
    expect(paths(sent)).toEqual([POLL, POLL]);
    const keys = sent.map((request) => request.headers()['idempotency-key']);
    expect(keys[0]).toBeTruthy();
    expect(keys[1]).toBeTruthy();
    expect(keys[0]).not.toBe(keys[1]);

    await page.getByRole('navigation', { name: 'Library' }).getByRole('link', { name: 'Downloads' }).click();
    await expect(page.getByRole('heading', { level: 2, name: 'Harbour.Lights.S02E08.1080p.WEB.h264' })).toBeVisible();
  });

  test('Check now is not offered with nothing followed', async ({ page }) => {
    await useScenario(page, { media: { following: 'empty' } });
    await gotoShell(page, '/apps/media/following');
    await expect(page.getByRole('heading', { name: 'Not following anything' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Check now' })).toHaveCount(0);
  });

  test.describe('with a conflict answer', () => {
    test.use({ allowStatus: [409], allowConsole: unreachableConsole });

    test('an unknown outcome says so and is not sent again after a reconnect', async ({ page }) => {
      await useScenario(page, {
        media: { refuse: { code: 'effect_unknown', message: 'The check did not confirm.' } },
      });
      const sent = writes(page);
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('button', { name: 'Check now' }).click();
      await expect(page.getByText('The outcome is unknown')).toBeVisible();
      await expect(
        page.getByText('The check did not confirm. atrium reloaded the list and did not try again.'),
      ).toBeVisible();
      expect(paths(sent)).toEqual([POLL]);
      await reconnect(page, /\/web\/v1\/media\/channels$/);
      expect(paths(sent)).toEqual([POLL]);
    });

    test('a check that stopped part way says some episodes were sent', async ({ page }) => {
      await useScenario(page, { media: { poll: 'partial' } });
      const sent = writes(page);
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('button', { name: 'Check now' }).click();
      await expect(page.getByText('The check stopped part way')).toBeVisible();
      await expect(
        page.getByText(
          'Channel poll stopped after adding torrents. Some new episodes were sent to Downloads. atrium did not try again.',
        ),
      ).toBeVisible();
      expect(paths(sent)).toEqual([POLL]);
    });
  });

  test.describe('unreachable', () => {
    test.use({ allowConsole: unreachableConsole });

    test('Check now is refused at once and nothing is sent', async ({ page }) => {
      const sent = writes(page);
      await gotoShell(page, '/apps/media/following');
      const check = page.getByRole('button', { name: 'Check now' });
      await expect(check).toBeVisible();
      await goUnreachable(page);
      await expect(check).toHaveAttribute('aria-disabled', 'true');
      await check.click({ force: true });
      await expect(page.getByText('Cannot reach atrium').first()).toBeVisible();
      expect(paths(sent)).toEqual([]);
    });
  });

  test('every catalogue title and the title page show their art', async ({ page }) => {
    await gotoShell(page, '/apps/media');
    await expect(page.locator('.lib-grid img.lib-poster__img')).toHaveCount(5);
    await loaded(page, '.lib-grid');
    await tile(page, 'Harbour Lights').getByRole('link').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Harbour Lights' })).toBeVisible();
    await expect(page.locator('.lib-item__art img')).toHaveAttribute('fetchpriority', 'high');
    await loaded(page, '.lib-item__art');
  });

  test.describe('with missing and failed art', () => {
    test.use({ allowStatus: [404, 503] });

    test('missing art stays quiet and failed art offers Retry', async ({ page }) => {
      await useScenario(page, { media: { art: { none: ['m2'], down: ['s2'] } } });
      await gotoShell(page, '/apps/media');
      const retry = tile(page, 'Lantern Street').getByRole('button', { name: 'Retry the art for Lantern Street' });
      await expect(retry).toBeVisible();
      await expect(tile(page, 'Paper Boats').locator('.lib-poster[data-state="none"]')).toBeVisible();
      await expect(tile(page, 'Paper Boats').getByRole('button')).toHaveCount(0);
      await loaded(page, '.lib-grid');
      await healArt(page);
      await retry.click();
      await expect(tile(page, 'Lantern Street').locator('.lib-poster[data-state="shown"]')).toBeVisible();
      await expect(retry).toHaveCount(0);
    });

    test('failed art on the title page offers Retry', async ({ page }) => {
      await useScenario(page, { media: { art: { down: ['s1'] } } });
      await gotoShell(page, '/apps/media/item/show/s1');
      const retry = page.getByRole('button', { name: 'Retry the art for Harbour Lights' });
      await expect(retry).toHaveText('Retry');
      await healArt(page);
      await retry.click();
      await expect(page.locator('.lib-item__art .lib-poster[data-state="shown"]')).toBeVisible();
    });
  });
});

const states: { name: string; scope?: string; setup: (page: Page) => Promise<void> }[] = [
  {
    name: 'show search results',
    scope: '.pop:popover-open',
    setup: async (page) => {
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('combobox', { name: 'Find on TMDB' }).fill('harb');
      await expect(page.getByRole('option')).toHaveCount(2);
    },
  },
  {
    name: 'release preview',
    setup: async (page) => {
      await gotoShell(page, '/apps/media/following');
      await pickShow(page, 'harb', 'Harbour Lights\\s*2023');
      await page.getByLabel('Release name pattern').fill('S02E08');
      await expect(releases(page).getByText('2 of 4 recent releases match')).toBeVisible();
    },
  },
  {
    name: 'check result',
    setup: async (page) => {
      await useScenario(page, { media: { poll: 'partial' } });
      await gotoShell(page, '/apps/media/following');
      await page.getByRole('button', { name: 'Check now' }).click();
      await expect(page.getByText('The check stopped part way')).toBeVisible();
    },
  },
  {
    name: 'catalogue art',
    setup: async (page) => {
      await useScenario(page, { media: { art: { none: ['m2'], down: ['s2'] } } });
      await gotoShell(page, '/apps/media');
      await expect(page.getByRole('button', { name: 'Retry the art for Lantern Street' })).toBeVisible();
      await expect(tile(page, 'The Quiet Orchard').locator('.lib-poster[data-state="shown"]')).toBeVisible();
    },
  },
  {
    name: 'title art',
    setup: async (page) => {
      await useScenario(page, { media: { art: { down: ['s1'] } } });
      await gotoShell(page, '/apps/media/item/show/s1');
      await expect(page.getByRole('button', { name: 'Retry the art for Harbour Lights' })).toBeVisible();
    },
  },
];

test.describe('library follow layouts', () => {
  test.use({ allowStatus: [404, 409, 503] });

  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const state of states) {
        test(`${state.name} fits, rounds and passes axe`, async ({ page }) => {
          await state.setup(page);
          await expectNoSeriousAxeFindings(page);
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page, state.scope);
          await expectNoBlue(page);
        });
      }
    });
  }
});
