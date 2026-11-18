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

function card(page: Page, id: string) {
  return page.locator(`[data-app="${id}"]`);
}

function appSwitch(page: Page, name: string) {
  return page.getByRole('switch', { name: new RegExp(`^${name}(\\s|$)`) });
}

test.describe('the apps hub', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('lists the apps that are on with their pages', async ({ page }) => {
    await gotoShell(page, '/apps');
    await expect(page.getByRole('heading', { level: 1, name: 'Apps' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Apps', exact: true });
    await expect(list.getByRole('heading', { level: 2 })).toHaveText([
      'Mail',
      'Calendar',
      'Torrents',
      'Library',
      'Orchard',
    ]);
    await expect(card(page, 'grafana')).toHaveCount(0);
    const mail = card(page, 'mail');
    await expect(mail.getByRole('link', { name: 'Decisions', exact: true })).toHaveAttribute(
      'href',
      '/apps/mail/decisions',
    );
    await expect(mail.getByRole('link', { name: /^Mail decisions/ })).toHaveCount(0);
    await expect(mail.getByRole('link', { name: 'Mail settings', exact: true })).toHaveAttribute(
      'href',
      '/settings/mail',
    );
    await expect(card(page, 'calendar').getByRole('link', { name: 'Calendar settings', exact: true })).toHaveAttribute(
      'href',
      '/settings/calendar',
    );
    const media = card(page, 'media');
    for (const [name, href] of [
      ['Downloads', '/apps/torrents'],
      ['Channels', '/apps/media/following'],
      ['Library', '/apps/media'],
    ] as const) {
      await expect(media.getByRole('link', { name, exact: true })).toHaveAttribute('href', href);
    }
    for (const link of await list.getByRole('link').all()) {
      expect(await link.getAttribute('href')).not.toMatch(
        /^\/(?:triage|rules|live|watches|apps\/media\/(?:downloads|channels|library))/,
      );
    }
  });

  test('an unmet need shows its state and links to the repair', async ({ page }) => {
    await gotoShell(page, '/apps');
    const media = card(page, 'media');
    await expect(media.getByText('Needs settings')).toBeVisible();
    await expect(media.getByText('The library server refuses the saved key.')).toBeVisible();
    await expect(media.getByText('HTTP 401 from /System/Info')).toBeVisible();
    await expect(media.getByRole('link', { name: /^Repair/ })).toHaveAttribute(
      'href',
      /\/settings\/connections\/media\/jellyfin$/,
    );
  });

  test('an app the client does not know opens as a generic page', async ({ page }) => {
    await gotoShell(page, '/apps/orchard');
    await expect(page.getByRole('heading', { level: 1, name: 'Orchard' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: 'Nothing to show here' })).toBeVisible();
    await expect(page.getByText('Orchard works through the assistant. Ask for it in a chat.')).toBeVisible();
    await page.getByRole('link', { name: 'App settings' }).click();
    await expect(page).toHaveURL(/\/apps\/directory#app-orchard$/);
    await expect(appSwitch(page, 'Orchard')).toBeVisible();
    await gotoShell(page, '/apps/grafana');
    await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
  });

  test('with every app off the hub points to the directory', async ({ page }) => {
    await useScenario(page, { apps: { off: ['mail', 'calendar', 'torrents', 'media', 'orchard'] } });
    await gotoShell(page, '/apps');
    await expect(page.getByText('No apps are on')).toBeVisible();
    await page.getByRole('link', { name: 'Open the directory' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'All apps' })).toBeVisible();
  });
});

test.describe('the apps directory', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409, 412] });

  test('turning an app on and off updates the rail', async ({ page }) => {
    await gotoShell(page, '/apps/directory');
    const rail = page.getByRole('navigation', { name: 'Main' });
    await expect(rail.getByRole('link', { name: 'Server ops' })).toHaveCount(0);
    await appSwitch(page, 'Server ops').click();
    await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'true');
    await expect(rail.getByRole('link', { name: 'Server ops' })).toBeVisible();
    await appSwitch(page, 'Server ops').click();
    await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'false');
    await expect(rail.getByRole('link', { name: 'Server ops' })).toHaveCount(0);
  });

  test('the keyboard alone switches an app', async ({ page }) => {
    await gotoShell(page, '/apps/directory');
    await appSwitch(page, 'Orchard').focus();
    await page.keyboard.press('Space');
    await expect(appSwitch(page, 'Orchard')).toHaveAttribute('aria-checked', 'false');
    await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Orchard' })).toHaveCount(0);
  });

  test('a host pin and an unmet dependency lock the switch', async ({ page }) => {
    await useScenario(page, { apps: { off: ['mail'] } });
    await gotoShell(page, '/apps/directory');
    await expect(appSwitch(page, 'Calendar')).toHaveAttribute('aria-disabled', 'true');
    await expect(card(page, 'calendar').getByText('Set by the host')).toBeVisible();
    await expect(appSwitch(page, 'Packages')).toHaveAttribute('aria-disabled', 'true');
    await expect(card(page, 'packages').getByText('Turn on Mail first')).toBeVisible();
    await card(page, 'packages').getByRole('link', { name: 'Open Mail' }).click();
    await expect(page).toHaveURL(/#app-mail$/);
  });

  test('turning off an app another app needs is refused and the switch reverts', async ({ page }) => {
    await useScenario(page, { apps: { on: ['packages'] } });
    await gotoShell(page, '/apps/directory');
    await appSwitch(page, 'Mail').click();
    await expect(card(page, 'mail').getByText('Another app needs it')).toBeVisible();
    await expect(card(page, 'mail').getByText('Packages needs Mail. Turn it off first.')).toBeVisible();
    await expect(appSwitch(page, 'Mail')).toHaveAttribute('aria-checked', 'true');
  });

  test('every refusal reason reads as its own title', async ({ page }) => {
    const reasons: [string, string][] = [
      ['core_unit', 'Always on'],
      ['pinned', 'Set by the host'],
      ['dependency_off', 'Needs another app'],
      ['needs_settings', 'Needs settings'],
      ['unavailable', 'Unavailable'],
    ];
    for (const [code, title] of reasons) {
      await useScenario(page, { apps: { refuse: { code, message: `Refused as ${code}.` } } });
      await gotoShell(page, '/apps/directory');
      await appSwitch(page, 'Server ops').click();
      const grafana = card(page, 'grafana');
      await expect(grafana.getByText(title, { exact: true })).toBeVisible();
      await expect(grafana.getByText(`Refused as ${code}.`)).toBeVisible();
      await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'false');
    }
  });

  test('a stale revision reloads and says so', async ({ page }) => {
    await useScenario(page, { apps: { stale: true } });
    await gotoShell(page, '/apps/directory');
    await appSwitch(page, 'Server ops').click();
    await expect(card(page, 'grafana').getByText('This changed since it loaded')).toBeVisible();
    await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'false');
    await appSwitch(page, 'Server ops').click();
    await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'true');
  });
});

test.describe('apps offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('a switch is refused without sending', async ({ page }) => {
    await gotoShell(page, '/apps/directory');
    await expect(appSwitch(page, 'Server ops')).toBeVisible();
    await goUnreachable(page);
    await appSwitch(page, 'Server ops').click();
    await expect(card(page, 'grafana').getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    await expect(appSwitch(page, 'Server ops')).toHaveAttribute('aria-checked', 'false');
  });
});

test.describe('the automation pause', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('the shell shows a paused badge that resumes', async ({ page }) => {
    await useScenario(page, { pause: { paused: true, reason: 'Paused for the garden club move.' } });
    await gotoShell(page, '/');
    const badge = page.locator('.pause-slot');
    await expect(badge.getByText('Automation paused')).toBeVisible();
    await expect(badge.getByText('Paused for the garden club move.')).toBeVisible();
    await badge.getByRole('button', { name: 'Resume' }).click();
    await expect(badge).toHaveCount(0);
  });

  test('Authority pauses and resumes automation', async ({ page }) => {
    await gotoShell(page, '/assistant/authority');
    const section = page.locator('#automation');
    await expect(section.getByText('Running')).toBeVisible();
    await section.getByRole('button', { name: 'Pause automation' }).click();
    await expect(section.getByText('Paused', { exact: true })).toBeVisible();
    await expect(page.locator('.pause-slot').getByText('Automation paused')).toBeVisible();
    await section.getByRole('button', { name: 'Resume automation' }).click();
    await expect(section.getByText('Running')).toBeVisible();
    await expect(page.locator('.pause-slot')).toHaveCount(0);
  });
});

test.describe('the automation pause with a lost answer', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('a resume from the shell whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/automation/resume',
      open: '/',
      scenario: { pause: { paused: true } },
      press: (page) => page.locator('.pause-slot').getByRole('button', { name: 'Resume' }).click(),
    });
  });

  test('a resume from Authority whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/automation/resume',
      open: '/assistant/authority',
      scenario: { pause: { paused: true } },
      press: (page) => page.locator('#automation').getByRole('button', { name: 'Resume automation' }).click(),
    });
  });
});

test.describe('apps design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of ['/apps', '/apps/directory', '/apps/orchard']) {
        test(`${path} fits, rounds and passes axe`, async ({ page }) => {
          await useScenario(page, { pause: { paused: true } });
          await gotoShell(page, path);
          await expect(page.locator('.app-grid, .card, .statebox').first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});
