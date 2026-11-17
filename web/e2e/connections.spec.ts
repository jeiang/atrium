import type { Page } from '@playwright/test';
import type { ConnectionRequest } from '../src/mocks/connections.ts';
import {
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';
import { expectLostAnswer } from './lost.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const secret = 'fictional-key-4471';

function requests(page: Page): Promise<ConnectionRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumConnectionRequests') ?? []);
}

async function openConnection(page: Page, id: string, title: string): Promise<void> {
  await gotoShell(page, `/settings/connections/${id}`);
  await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible();
}

test.describe('the connections list', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('Settings links to every connection with its state, reason, owner, check time and use', async ({ page }) => {
    await gotoShell(page, '/settings');
    await page.getByRole('link', { name: 'Connections' }).click();
    await expect(page).toHaveURL(/\/settings\/connections$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Connections' })).toBeVisible();
    await expect(page.getByText('11 connections, 5 need attention')).toBeVisible();
    const table = page.getByRole('table', { name: 'Connections' });
    await expect(table.locator('tbody tr')).toHaveCount(11);

    const grace = table.locator('tr[data-key="media/qbittorrent"]');
    await expect(grace.locator('[data-state="failing"][data-grace="true"]')).toContainText(/Away until \d\d:\d\d/);
    await expect(grace).toContainText('connection refused');
    await expect(grace).toContainText('Host');
    await expect(grace).toContainText('downloads');

    await expect(table.locator('tr[data-key="media/jellyfin"] [data-state="failing"]')).toHaveText('Failing');
    await expect(table.locator('tr[data-key="auth/oidc"] [data-state="untested"]')).toHaveText('Untested');
    await expect(table.locator('tr[data-key="auth/oidc"]')).toContainText('its test needs an interactive sign-in');
    await expect(table.locator('tr[data-key="service/webpush"]')).toContainText('No capability has been tested yet.');
    await expect(table.locator('tr[data-key="notion/workspace"] [data-state="disabled"]')).toHaveText('Disabled');
    await expect(table.locator('tr[data-key="mail/garden-club"] [data-state="unconfigured"]')).toHaveText('Not set up');
    await expect(table.locator('tr[data-key="google/default"] [data-state="needs_reauth"]')).toHaveText(
      'Needs sign-in',
    );
    await expect(table.locator('tr[data-key="service/embedding"] [data-state="expiring"]')).toHaveText('Expiring');
    await expect(table.locator('tr[data-key="service/ship24"]')).toContainText('You');
    await expect(table.locator('tr[data-key="auth/oidc"]')).toContainText('Never');

    await grace.click();
    await expect(page).toHaveURL(/\/settings\/connections\/media\/qbittorrent$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Downloads box' })).toBeVisible();
    await expect(page.getByText(/atrium keeps using it until/)).toBeVisible();
  });

  test('an empty store says no connections', async ({ page }) => {
    await useScenario(page, { connections: { empty: true } });
    await gotoShell(page, '/settings/connections');
    await expect(page.getByRole('heading', { name: 'No connections' })).toBeVisible();
  });

  test('an unknown address says no such connection', async ({ page }) => {
    await gotoShell(page, '/settings/connections/service/nowhere');
    await expect(page.getByRole('heading', { name: 'No such connection' })).toBeVisible();
  });
});

test.describe('a connection page', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409, 422] });

  test('host fields are read-only and say why, and a host slot is provided by the host', async ({ page }) => {
    await openConnection(page, 'service/weather', 'Open-Meteo');
    const fields = page.locator('#connection-fields');
    await expect(fields.getByRole('textbox', { name: 'Name' })).toHaveAttribute('readonly', '');
    await expect(fields.getByRole('textbox', { name: 'Address' })).toHaveValue(
      'https://api.open-meteo.com/v1/forecast',
    );
    await expect(fields.getByRole('textbox', { name: 'Address' })).toHaveAttribute('readonly', '');
    await expect(fields.getByText('Set by the host').first()).toBeVisible();
    await expect(fields).toContainText('https://geocoding-api.open-meteo.com/v1/search');
    await expect(fields.getByRole('button', { name: 'Test and save' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: /^Disconnect/ })).toHaveCount(0);

    await openConnection(page, 'service/anthropic', 'Anthropic');
    await expect(page.locator('#connection-fields')).toContainText(
      'Provided by the host from /run/secrets/anthropic-key.',
    );
    await expect(page.locator('#connection-fields input[type="password"]')).toHaveCount(0);
  });

  test('an operator field saves through Test and save with a visible result', async ({ page }) => {
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const fields = page.locator('#connection-fields');
    const save = fields.getByRole('button', { name: 'Test and save' });
    await expect(save).toHaveAttribute('aria-disabled', 'true');
    await fields.getByRole('textbox', { name: 'Name' }).fill('Parcel tracker');
    await save.click();
    await expect(fields.getByText('Tested and saved')).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Parcel tracker' })).toBeVisible();
    const sent = (await requests(page)).filter((item) => item.method === 'PUT');
    expect(sent).toHaveLength(1);
    expect(sent[0]?.path).toBe('/web/v1/connections/service/ship24');
    expect(sent[0]?.key).toMatch(uuid);
    expect(sent[0]?.body).toEqual({ label: 'Parcel tracker' });
  });

  test('a stale save says so, reloads and keeps the draft', async ({ page }) => {
    await useScenario(page, { connections: { conflict: true } });
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const fields = page.locator('#connection-fields');
    await fields.getByRole('textbox', { name: 'Name' }).fill('Parcel tracker');
    await fields.getByRole('button', { name: 'Test and save' }).click();
    await expect(fields.getByText('This connection changed since it loaded')).toBeVisible();
    await expect(fields.getByRole('textbox', { name: 'Name' })).toHaveValue('Parcel tracker');
  });

  test('a failed candidate test saves nothing and keeps the draft', async ({ page }) => {
    await useScenario(page, { connections: { saveTest: 'fail' } });
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const fields = page.locator('#connection-fields');
    await fields.getByRole('textbox', { name: 'Address' }).fill('https://parcels.example.test/v2');
    await fields.getByRole('button', { name: 'Test and save' }).click();
    await expect(fields.getByText('Not saved: the test failed')).toBeVisible();
    await expect(fields).toContainText('HTTP 401 from /v1/trackers');
    await expect(fields.getByRole('textbox', { name: 'Address' })).toHaveValue('https://parcels.example.test/v2');
  });

  test('a credential is set without ever being shown', async ({ page }) => {
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const fields = page.locator('#connection-fields');
    const key = fields.getByLabel('API key', { exact: true });
    await expect(key).toHaveValue('');
    await expect(key).toHaveAttribute('type', 'password');
    await expect(key).toHaveAttribute('autocomplete', 'new-password');
    await expect(fields).toContainText(/Stored .* by operator@example.test/);
    await key.fill(secret);
    await fields.getByRole('button', { name: 'Show API key' }).click();
    await expect(key).toHaveAttribute('type', 'text');
    await fields.getByRole('button', { name: 'Hide API key' }).click();
    await expect(key).toHaveAttribute('type', 'password');
    await fields.getByRole('button', { name: 'Save without testing' }).click();
    await expect(fields.getByText('Saved without testing')).toBeVisible();
    await expect(key).toHaveValue('');
    await expect(page.locator('[data-state="untested"]').first()).toBeVisible();
    expect(await page.locator('body').innerText()).not.toContain(secret);
    const sent = (await requests(page)).filter((item) => item.method === 'PATCH');
    expect(sent).toHaveLength(1);
    expect(sent[0]?.key).toMatch(uuid);
    expect(sent[0]?.body).toEqual({ credentials: { api_key: secret } });
    const listed = await page.evaluate(async () => (await fetch('/web/v1/connections')).text());
    expect(listed).not.toContain(secret);
  });

  test('Test now shows pending, then success', async ({ page }) => {
    await openConnection(page, 'media/jellyfin', 'Living room media');
    const status = page.locator('#connection-status');
    await status.getByRole('button', { name: 'Test now' }).click();
    await expect(status.getByRole('status').filter({ hasText: 'Testing Living room media' })).toBeVisible();
    await expect(status.getByText('Test passed')).toBeVisible();
    await expect(status.locator('[data-state="ok"]')).toHaveText('OK');
    await expect(page.locator('#connection-results [data-state="ok"]:visible')).toHaveCount(2);
    const sent = (await requests(page)).filter((item) => item.path.endsWith('/test'));
    expect(sent).toHaveLength(1);
    expect(sent[0]?.key).toMatch(uuid);
  });

  test('Test now shows a failing result with its error text', async ({ page }) => {
    await useScenario(page, { connections: { test: 'fail' } });
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const status = page.locator('#connection-status');
    await status.getByRole('button', { name: 'Test now' }).click();
    await expect(status.getByText('Test result: Failing')).toBeVisible();
    await expect(status).toContainText('dial tcp 192.0.2.44:443: i/o timeout');
    await expect(page.locator('#connection-results')).toContainText('i/o timeout');
  });

  test('a disabled connection states the consequence and offers no test', async ({ page }) => {
    await openConnection(page, 'notion/workspace', 'Notes workspace');
    const status = page.locator('#connection-status');
    await expect(status).toContainText(
      'atrium does not use or test it until the host file or its app turns it back on.',
    );
    await expect(status.getByRole('button', { name: 'Test now' })).toHaveAttribute('aria-disabled', 'true');
    await expect(status.getByText('The connection is off')).toBeVisible();
  });

  test('repair guidance follows the state', async ({ page }) => {
    await openConnection(page, 'google/default', 'Google');
    await expect(page.getByText('Connect Google again in Google setup below, then Test now.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open Connections in the current interface' })).toHaveCount(0);
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    await expect(page.locator('#google-setup')).toHaveCount(0);
    await openConnection(page, 'mail/garden-club', 'Garden club mail');
    await expect(page.getByText('Fill in what is missing below, then choose Test and save.')).toBeVisible();
    await expect(page.locator('#connection-fields')).toContainText('Not set.');
  });

  test('a pinned slot has no entry box and a shared slot links to its owner', async ({ page }) => {
    await useScenario(page, { connections: { sharedSlot: true } });
    await openConnection(page, 'mail/harbor', 'Harbor mail');
    const fields = page.locator('#connection-fields');
    await expect(fields).toContainText('Provided by the host from /run/secrets/harbor-mail.');
    await expect(fields.locator('input[type="password"]')).toHaveCount(0);
    await expect(page.getByText('The credential is changed in host configuration.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open Connections in the current interface' })).toHaveCount(0);

    await openConnection(page, 'icloud/harbor', 'Harbor calendar');
    await expect(fields).toContainText('Uses the credential of mail/harbor. It is set in host configuration.');
    await expect(fields.locator('input[type="password"]')).toHaveCount(0);
    await expect(fields).not.toContainText('/run/secrets/harbor-mail');
    await expect(page.getByText('This connection uses the credential of mail/harbor.')).toBeVisible();
    await fields.getByRole('link', { name: 'Open mail/harbor' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Harbor mail' })).toBeVisible();
  });

  test('a save over a pinned slot is refused and saves nothing', async ({ page }) => {
    await useScenario(page, { connections: { pinnedRace: true } });
    await openConnection(page, 'mail/garden-club', 'Garden club mail');
    const fields = page.locator('#connection-fields');
    await fields.getByLabel('Password', { exact: true }).fill(secret);
    await fields.getByRole('button', { name: 'Test and save' }).click();
    await expect(fields.getByText('This credential is set in host configuration')).toBeVisible();
    await expect(fields).toContainText('atrium saved nothing.');
    await expect(fields.getByText('This connection changed since it loaded')).toHaveCount(0);
    expect(await page.locator('body').innerText()).not.toContain(secret);
  });

  test('disconnect asks first, then clears the operator connection', async ({ page }) => {
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    await page.getByRole('button', { name: 'Disconnect Ship24 parcels' }).click();
    const dialog = page.getByRole('dialog', { name: 'Disconnect Ship24 parcels?' });
    await expect(dialog).toContainText('This deletes the stored API key');
    await expect(dialog).toContainText('It cannot be undone.');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    expect((await requests(page)).filter((item) => item.method === 'DELETE')).toHaveLength(0);

    await page.getByRole('button', { name: 'Disconnect Ship24 parcels' }).click();
    await dialog.getByRole('button', { name: 'Disconnect', exact: true }).click();
    await expect(page).toHaveURL(/\/settings\/connections$/);
    await expect(page.getByText('Disconnected Ship24 parcels.')).toBeVisible();
    const row = page.locator('tr[data-key="service/ship24"]');
    await expect(row.locator('[data-state="unconfigured"]')).toHaveText('Not set up');
    const removed = (await requests(page)).filter((item) => item.method === 'DELETE');
    expect(removed).toHaveLength(1);
    expect(removed[0]?.key).toMatch(uuid);
  });
});

test.describe('server errors', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [500] });

  test('a failed load offers Retry', async ({ page }) => {
    await useScenario(page, { connections: { fail: true } });
    await gotoShell(page, '/settings/connections');
    await expect(page.getByRole('heading', { name: 'Connections did not load' })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
  });

  test('Test now shows a refusal when atrium cannot run it', async ({ page }) => {
    await useScenario(page, { connections: { test: 'error' } });
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    const status = page.locator('#connection-status');
    await status.getByRole('button', { name: 'Test now' }).click();
    await expect(status.getByText('atrium refused the test')).toBeVisible();
  });
});

test.describe('offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('the cached page reads and a test is refused without sending', async ({ page }) => {
    await openConnection(page, 'service/ship24', 'Ship24 parcels');
    await goUnreachable(page);
    const status = page.locator('#connection-status');
    await expect(status.locator('[data-state="ok"]')).toBeVisible();
    await status.getByRole('button', { name: 'Test now' }).click();
    await expect(status.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    expect((await requests(page)).filter((item) => item.path.endsWith('/test'))).toHaveLength(0);
  });
});

test.describe('lost answers', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('a credential save whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'PATCH',
      path: '/connections/service/ship24',
      open: '/settings/connections/service/ship24',
      press: async (page) => {
        const fields = page.locator('#connection-fields');
        await fields.getByLabel('API key', { exact: true }).fill(secret);
        await fields.getByRole('button', { name: 'Save without testing' }).click();
      },
    });
  });

  test('a test whose answer is lost reads as unknown and is never sent again', async ({ page }) => {
    await expectLostAnswer(page, {
      method: 'POST',
      path: '/connections/media/jellyfin/test',
      open: '/settings/connections/media/jellyfin',
      press: (page) => page.locator('#connection-status').getByRole('button', { name: 'Test now' }).click(),
    });
  });
});

test.describe('the weather location', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [412] });

  test('validates after interaction, saves with If-Match and Home shows the new place', async ({ page }) => {
    await openConnection(page, 'service/weather', 'Open-Meteo');
    const form = page.locator('#weather-location');
    const latitude = form.getByRole('textbox', { name: 'Latitude' });
    await expect(form.getByRole('textbox', { name: 'Place name' })).toHaveValue('Hillfield');
    await latitude.fill('91');
    await expect(form.getByText('The latitude must be between -90 and 90.')).toHaveCount(0);
    await latitude.blur();
    await expect(form.getByText('The latitude must be between -90 and 90.')).toBeVisible();
    await latitude.fill('50.9');
    await expect(form.getByText('The latitude must be between -90 and 90.')).toHaveCount(0);
    await form.getByRole('textbox', { name: 'Place name' }).fill('Saltmarsh Bay');
    await form.getByRole('button', { name: 'Save location' }).click();
    await expect(form.getByText('Location saved')).toBeVisible();
    const sent = (await requests(page)).filter((item) => item.path === '/web/v1/settings');
    expect(sent).toHaveLength(1);
    expect(sent[0]?.ifMatch).toBe('W/"7"');
    expect(sent[0]?.key).toMatch(uuid);
    expect(sent[0]?.body).toEqual({ set: { weather_location_name: 'Saltmarsh Bay', weather_latitude: '50.9' } });

    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /^Home/ }).click();
    await expect(page.getByRole('heading', { level: 2, name: 'Weather in Saltmarsh Bay' })).toBeVisible();
  });

  test('a stale save says so, reloads and keeps the draft', async ({ page }) => {
    await useScenario(page, { connections: { settingsStale: true } });
    await openConnection(page, 'service/weather', 'Open-Meteo');
    const form = page.locator('#weather-location');
    await form.getByRole('textbox', { name: 'Place name' }).fill('Saltmarsh Bay');
    await form.getByRole('button', { name: 'Save location' }).click();
    await expect(form.getByText('The settings changed since they loaded')).toBeVisible();
    await expect(form.getByRole('textbox', { name: 'Place name' })).toHaveValue('Saltmarsh Bay');
    await form.getByRole('button', { name: 'Save location' }).click();
    await expect(form.getByText('Location saved')).toBeVisible();
  });

  test('a key pinned by the host is read-only and says why', async ({ page }) => {
    await useScenario(page, { connections: { pinned: true } });
    await openConnection(page, 'service/weather', 'Open-Meteo');
    const form = page.locator('#weather-location');
    await expect(form.getByRole('textbox', { name: 'Place name' })).toHaveAttribute('readonly', '');
    await expect(form.getByText('Set by the host')).toBeVisible();
  });
});

test.describe('connections design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of [
        '/settings/connections',
        '/settings/connections/service/ship24',
        '/settings/connections/service/weather',
      ]) {
        test(`${path} fits, rounds, stays off blue and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(page.locator('[data-state]:visible').first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoBlue(page);
          await expectNoSeriousAxeFindings(page);
        });
      }

      test('the disconnect dialog passes axe', async ({ page }) => {
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await gotoShell(page, '/settings/connections/service/ship24');
        await page.getByRole('button', { name: 'Disconnect Ship24 parcels' }).click();
        await expect(page.locator('dialog[open]')).toBeVisible();
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoSeriousAxeFindings(page);
      });

      for (const path of ['/settings/connections/mail/harbor', '/settings/connections/icloud/harbor']) {
        test(`${path} with a pinned or shared slot fits, rounds, stays off blue and passes axe`, async ({ page }) => {
          await useScenario(page, { connections: { sharedSlot: true } });
          await gotoShell(page, path);
          await expect(page.locator('[data-state]:visible').first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoBlue(page);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});
