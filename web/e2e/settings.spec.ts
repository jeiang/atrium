import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
import { expectNoHorizontalScroll, expectNoSeriousAxeFindings, expectTouchTargets, gotoShell } from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

interface SettingsRequest {
  method: string;
  path: string;
  key: string | null;
  ifMatch: string | null;
  body: unknown;
}

const schema = JSON.parse(readFileSync(new URL('../../docs/api/settings-schema.json', import.meta.url), 'utf8')) as {
  pages: { fields: { key: string; group: string }[] }[];
};

const groupPages = [
  'general',
  'sign-in',
  'connection-details',
  'apps',
  'mail',
  'calendar',
  'library',
  'plan',
  'knowledge-export',
];

async function requests(page: Page): Promise<SettingsRequest[]> {
  return page.evaluate(
    () => (globalThis as { atriumSettingsRequests?: SettingsRequest[] }).atriumSettingsRequests ?? [],
  );
}

async function patches(page: Page): Promise<SettingsRequest[]> {
  return (await requests(page)).filter((item) => item.method === 'PATCH');
}

const form = (page: Page) => page.locator('form.settings-form');

test.describe('settings on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409, 412, 422] });

  test('the index shows the group list beside General', async ({ page }) => {
    await gotoShell(page, '/settings');
    const nav = page.getByRole('navigation', { name: 'Settings pages' });
    for (const name of ['General', 'Sign-in', 'Connection details', 'Apps', 'Mail', 'Calendar', 'Backups']) {
      await expect(nav.getByRole('link', { name, exact: true })).toBeVisible();
    }
    await expect(nav.getByRole('link', { name: 'Connections', exact: true })).toHaveAttribute(
      'href',
      /\/settings\/connections$/,
    );
    await expect(page.getByRole('heading', { name: 'General', level: 2 })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Set on the weather connection' })).toHaveAttribute(
      'href',
      /\/settings\/connections\/service\/weather$/,
    );
  });

  test('a save sends only the change with one key and the read revision', async ({ page }) => {
    await gotoShell(page, '/settings/general');
    await page.getByLabel('Time zone').fill('Europe/London');
    await page.getByRole('button', { name: 'Save' }).click();
    const notice = page.locator('.settings-form__result');
    await expect(notice).toContainText(/Saved at \d\d:\d\d/);
    await expect(notice).toContainText('Changed Time zone.');
    await expect(notice).toContainText('Restart atrium to apply Time zone');
    await expect(page.getByRole('button', { name: 'Save' })).toHaveCount(0);
    const sent = await patches(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.body).toEqual({ set: { tz: 'Europe/London' } });
    expect(sent[0]?.ifMatch).toBe('W/"11"');
    expect(sent[0]?.key).toBeTruthy();
  });

  test('a change made elsewhere keeps the draft and marks the setting', async ({ page }) => {
    await useScenario(page, { settings: { elsewhere: { write_cap: '50' } } });
    await gotoShell(page, '/settings/mail');
    await page.getByLabel('Automatic write cap').fill('80');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.settings-form__result')).toContainText('The settings changed elsewhere');
    await expect(page.getByLabel('Automatic write cap')).toHaveValue('80');
    const row = page.locator('[data-key="write_cap"]');
    await expect(row).toContainText('Changed elsewhere');
    await expect(row).toContainText('Now 50');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.settings-form__result')).toContainText(/Saved at/);
    const sent = await patches(page);
    expect(sent.map((item) => item.key)).toHaveLength(2);
    expect(new Set(sent.map((item) => item.key)).size).toBe(2);
    expect(sent[1]?.ifMatch).not.toBe(sent[0]?.ifMatch);
  });

  test('a refused value shows at the setting and nothing is saved', async ({ page }) => {
    await useScenario(page, { settings: { refuse: { oidc_session_idle_days: 'must be at most 90' } } });
    await gotoShell(page, '/settings/sign-in');
    await page.getByLabel('Sign out after idle (days)').fill('400');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.settings-form__result')).toContainText('Nothing was saved.');
    await expect(page.locator('[data-key="oidc_session_idle_days"]')).toContainText('must be at most 90');
  });

  test('a number field checks its input before saving', async ({ page }) => {
    await gotoShell(page, '/settings/calendar');
    await page.getByLabel('Event minutes').fill('ten');
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('[data-key="default_event_minutes"]')).toContainText('Enter a number.');
    expect(await patches(page)).toHaveLength(0);
  });

  test('a host-set setting is read-only and names its source', async ({ page }) => {
    await useScenario(page, { settings: { pinned: { tz: 'env ATRIUM_TZ' } } });
    await gotoShell(page, '/settings/general');
    const row = page.locator('[data-key="tz"]');
    await expect(row).toContainText('Set by the host in the environment variable ATRIUM_TZ');
    await expect(row.getByRole('textbox')).toHaveCount(0);
  });

  test('a stored value resets to its default', async ({ page }) => {
    await useScenario(page, { settings: { stored: { default_event_minutes: '45' } } });
    await gotoShell(page, '/settings/calendar');
    const row = page.locator('[data-key="default_event_minutes"]');
    await expect(row).toContainText('Default 60');
    await row.getByRole('button', { name: 'Reset to default' }).click();
    await expect(page.locator('.settings-form__result')).toContainText('Event minutes is back to its default.');
    await expect(page.getByLabel('Event minutes')).toHaveValue('60');
    expect((await patches(page))[0]?.body).toEqual({ unset: ['default_event_minutes'] });
  });

  test('the app switches are shown with their state and no control', async ({ page }) => {
    await gotoShell(page, '/settings/apps');
    const row = page.locator('[data-key="mail_enabled"]');
    await expect(row).toContainText('Mail app is on');
    await expect(row).toContainText('On');
    await expect(row).toContainText('Set in atrium');
    await expect(form(page).getByRole('switch')).toHaveCount(0);
    await expect(form(page).getByRole('link')).toHaveCount(0);
  });

  test('mail mode is shown with the way to change it', async ({ page }) => {
    await gotoShell(page, '/settings/mail');
    await expect(page.locator('#setting-mail_mode')).toContainText('atrium settings set mail_mode live');
  });

  test('every settings key outside Assistant and weather shows exactly once', async ({ page }) => {
    const expected = schema.pages
      .flatMap((item) => item.fields)
      .filter((field) => !field.group.startsWith('assistant.') && field.group !== 'connections.weather')
      .map((field) => field.key);
    const seen: string[] = [];
    for (const slug of groupPages) {
      await gotoShell(page, `/settings/${slug}`);
      await expect(form(page).locator('[data-key]').first()).toBeVisible();
      seen.push(
        ...(await form(page)
          .locator('[data-key]')
          .evaluateAll((rows) => rows.map((row) => row.dataset.key ?? ''))),
      );
    }
    expect(seen.toSorted()).toEqual([...new Set(expected)].toSorted());
  });

  test('a deep link opens the group and focuses the setting', async ({ page }) => {
    await gotoShell(page, '/settings?key=work_hours');
    await expect(page).toHaveURL(/\/settings\/calendar\?key=work_hours$/);
    await expect(page.getByLabel('Work hours')).toBeFocused();
  });

  test('leaving with unsaved changes asks first', async ({ page }) => {
    await gotoShell(page, '/settings/library');
    await page.getByLabel('Media link lifetime (seconds)').fill('600');
    await page.getByRole('navigation', { name: 'Settings pages' }).getByRole('link', { name: 'Plan' }).click();
    const dialog = page.getByRole('dialog', { name: 'Leave without saving?' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Keep editing' }).click();
    await expect(page).toHaveURL(/\/settings\/library$/);
    await expect(page.getByLabel('Media link lifetime (seconds)')).toHaveValue('600');
    await page.getByRole('navigation', { name: 'Settings pages' }).getByRole('link', { name: 'Plan' }).click();
    await page
      .getByRole('dialog', { name: 'Leave without saving?' })
      .getByRole('button', { name: 'Discard changes' })
      .click();
    await expect(page).toHaveURL(/\/settings\/plan$/);
  });

  test('an unknown group says so', async ({ page }) => {
    await gotoShell(page, '/settings/nowhere');
    await expect(page.getByText('No such settings page')).toBeVisible();
  });

  test('backups are listed, made and verified', async ({ page }) => {
    await gotoShell(page, '/settings/backups');
    const list = page.getByRole('list', { name: 'Backups' });
    await expect(list.getByRole('listitem')).toHaveCount(3);
    await expect(list.getByRole('listitem').nth(2)).toContainText('Made by an older atrium');
    await list
      .getByRole('listitem')
      .first()
      .getByRole('button', { name: /Verify/ })
      .click();
    await expect(list.getByRole('listitem').first()).toContainText(/Verified at \d\d:\d\d/);
    await page.getByRole('button', { name: 'Back up now' }).click();
    await expect(page.locator('.notice__title').filter({ hasText: /Backed up at \d\d:\d\d/ })).toBeVisible();
    await expect(list.getByRole('listitem')).toHaveCount(4);
    const posts = (await requests(page)).filter((item) => item.method === 'POST');
    expect(posts).toHaveLength(2);
    expect(posts.every((item) => item.key)).toBe(true);
  });

  test('backups explain a folder the host has not set', async ({ page }) => {
    await useScenario(page, { settings: { backups: 'unconfigured' } });
    await gotoShell(page, '/settings/backups');
    await expect(page.getByText('Backups are not set up')).toBeVisible();
    await expect(page.getByText('services.atrium.backup.directory')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Back up now' })).toHaveCount(0);
  });

  test('a failed verify says why', async ({ page }) => {
    await useScenario(page, { settings: { backups: 'verify-fail' } });
    await gotoShell(page, '/settings/backups');
    await page
      .getByRole('list', { name: 'Backups' })
      .getByRole('button', { name: /Verify/ })
      .first()
      .click();
    await expect(page.getByText('the archive key does not open it')).toBeVisible();
  });

  test('named archives are listed apart from the scheduled ones that pruning removes', async ({ page }) => {
    await gotoShell(page, '/settings/backups');
    await expect(page.getByRole('heading', { level: 3, name: 'Scheduled' })).toBeVisible();
    const kept = page.getByRole('list', { name: 'Kept backups' });
    await expect(page.getByRole('heading', { level: 3, name: 'Kept' })).toBeVisible();
    await expect(kept.getByRole('listitem')).toHaveCount(1);
    await expect(kept.getByRole('listitem')).toContainText('before-moving-house.tar.age');
    await expect(page.getByRole('list', { name: 'Backups' })).not.toContainText('before-moving-house');
    await expect(page.getByText(/not examined/)).toHaveCount(0);
  });

  test('files past the examine cap are counted', async ({ page }) => {
    await useScenario(page, { settings: { backups: 'capped' } });
    await gotoShell(page, '/settings/backups');
    await expect(page.getByText('214 more files in the folder were not examined.')).toBeVisible();
  });
});

test.describe('settings that do not load', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [500] });

  test('an error offers a retry', async ({ page }) => {
    await useScenario(page, { settings: { fail: true } });
    await gotoShell(page, '/settings/general');
    await expect(page.getByText('These settings did not load')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
  });

  test('an empty schema says there is nothing here', async ({ page }) => {
    await useScenario(page, { settings: { empty: true } });
    await gotoShell(page, '/settings/general');
    await expect(page.getByText('No settings here')).toBeVisible();
  });
});

test.describe('settings offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('an unreachable atrium is named and nothing is offered', async ({ page }) => {
    await gotoShell(page, '/');
    await goUnreachable(page);
    await page.evaluate(() => {
      history.pushState({}, '', '/settings/general');
      dispatchEvent(new PopStateEvent('popstate'));
    });
    await expect(form(page).getByText('Cannot reach atrium')).toBeVisible();
  });
});

test.describe('settings design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      test('a group page fits and passes checks', async ({ page }) => {
        await gotoShell(page, '/settings/calendar');
        await expect(page.getByLabel('Work hours')).toBeVisible();
        if (layout.width < 861) {
          await expect(page.getByRole('navigation', { name: 'Settings pages' })).toBeHidden();
          await expect(page.getByRole('link', { name: 'All settings' })).toBeVisible();
        } else {
          await expect(page.getByRole('navigation', { name: 'Settings pages' })).toBeVisible();
        }
        await expectNoHorizontalScroll(page);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });

      test('the index fits and passes checks', async ({ page }) => {
        await gotoShell(page, '/settings');
        await expect(page.getByRole('navigation', { name: 'Settings pages' })).toBeVisible();
        if (layout.width < 861) await expect(form(page)).toBeHidden();
        await expectNoHorizontalScroll(page);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });

      test('the backups page passes checks', async ({ page }) => {
        await gotoShell(page, '/settings/backups');
        await expect(page.getByRole('list', { name: 'Backups' })).toBeVisible();
        await expectNoHorizontalScroll(page);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});
