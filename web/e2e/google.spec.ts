import type { Page, Request } from '@playwright/test';
import type { ConnectionRequest } from '../src/mocks/connections.ts';
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
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const page_ = '/settings/connections/google/default';
const unknownReturn = `${page_}?state=web.unknown&code=fictional-code`;

function requests(page: Page): Promise<ConnectionRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumConnectionRequests') ?? []);
}

function completions(page: Page): Promise<ConnectionRequest[]> {
  return requests(page).then((sent) => sent.filter((item) => item.path.endsWith('/google/oauth/complete')));
}

function starts(page: Page): Request[] {
  const seen: Request[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && request.url().endsWith('/google/oauth/start')) seen.push(request);
  });
  return seen;
}

async function openGoogle(page: Page, path = page_): Promise<void> {
  await gotoShell(page, path);
  await expect(page.getByRole('heading', { level: 1, name: 'Google' })).toBeVisible();
}

async function consent(page: Page, label: string): Promise<void> {
  const setup = page.locator('#google-setup');
  await setup.getByRole('radio', { name: label }).check();
  await setup.getByRole('button', { name: 'Continue to Google' }).click();
  await expect(page).toHaveURL(/[?&]state=web\./);
  await expect(page.getByRole('heading', { level: 1, name: 'Google' })).toBeVisible();
}

test.describe('Google setup', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [422] });

  test('saves the client, leaves for consent, returns and finishes with one request per step', async ({ page }) => {
    await useScenario(page, { connections: { google: 'fresh' } });
    await openGoogle(page);
    const setup = page.locator('#google-setup');
    await expect(setup.getByRole('textbox', { name: 'Authorised redirect address' })).toHaveValue(
      /\/settings\/connections\/google\/callback$/,
    );
    await expect(page.getByText('Follow the steps in Google setup below, then Test now.')).toBeVisible();
    const proceed = setup.getByRole('button', { name: 'Continue to Google' });
    await expect(proceed).toHaveAttribute('aria-disabled', 'true');

    await setup.getByRole('textbox', { name: 'Client ID' }).fill('fictional-client');
    await setup.getByRole('textbox', { name: 'Client secret', exact: true }).fill('fictional-secret-2219');
    await setup.getByRole('button', { name: 'Save client details' }).click();
    await expect(setup.getByText('Client details saved')).toBeVisible();
    await expect(setup.getByText('Stored. A new value replaces it.')).toBeVisible();
    const saved = (await requests(page)).filter((item) => item.path === '/web/v1/connections/google/oauth');
    expect(saved).toHaveLength(1);
    expect(saved[0]?.method).toBe('PUT');
    expect(saved[0]?.key).toMatch(uuid);
    expect(saved[0]?.body).toEqual({ client_id: 'fictional-client', client_secret: 'fictional-secret-2219' });
    await expect(proceed).not.toHaveAttribute('aria-disabled');

    const started = starts(page);
    await consent(page, 'Gmail');
    expect(started).toHaveLength(1);
    expect(started[0]?.postDataJSON()).toEqual({ capability: 'gmail' });
    expect(started[0]?.headers()['idempotency-key']).toMatch(uuid);

    await expect(setup.getByRole('heading', { name: 'Finish connecting' })).toBeVisible();
    await setup.getByRole('button', { name: 'Finish connecting' }).click();
    await expect(setup.getByText('Connected Gmail')).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/connections\/google\/default$/);
    await expect(setup.getByRole('button', { name: 'Finish connecting' })).toHaveCount(0);
    await expect(setup.getByRole('radio', { name: 'Gmail' })).toHaveAccessibleDescription(/^Connected /);
    const sent = await completions(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.key).toMatch(uuid);
    expect(sent[0]?.body).toEqual({ state: expect.stringMatching(/^web\./), code: 'fictional-code-gmail' });
  });

  test('a missing Google row is reachable from the list and set up from its page', async ({ page }) => {
    await useScenario(page, { connections: { google: 'absent' } });
    await gotoShell(page, '/settings/connections');
    await page.getByRole('link', { name: 'Set up Google' }).click();
    await expect(page).toHaveURL(/\/settings\/connections\/google\/default$/);
    await expect(page.getByRole('heading', { name: 'Client details' })).toBeVisible();
  });

  test('a denied consent says so and sends nothing', async ({ page }) => {
    await useScenario(page, { connections: { google: 'denied' } });
    await openGoogle(page);
    await consent(page, 'Calendar');
    const setup = page.locator('#google-setup');
    await expect(setup.getByText('Google did not give consent')).toBeVisible();
    await expect(
      setup.getByText('Google answered access_denied. Nothing was connected.', { exact: false }),
    ).toBeVisible();
    await expect(setup.getByRole('button', { name: 'Finish connecting' })).toHaveCount(0);
    await setup.getByRole('button', { name: 'Dismiss' }).click();
    await expect(page).toHaveURL(/\/settings\/connections\/google\/default$/);
    await expect(setup.getByText('Google did not give consent')).toHaveCount(0);
    expect(await completions(page)).toHaveLength(0);
  });

  test('an unknown or used consent link is refused once and not offered again', async ({ page }) => {
    await openGoogle(page, unknownReturn);
    const setup = page.locator('#google-setup');
    await setup.getByRole('button', { name: 'Finish connecting' }).click();
    await expect(setup.getByText('atrium refused to finish connecting Google')).toBeVisible();
    await expect(setup.getByText('this consent link is unknown or already used; start again')).toBeVisible();
    await expect(setup.getByRole('button', { name: 'Finish connecting' })).toHaveCount(0);
    await expect(page).toHaveURL(/\/settings\/connections\/google\/default$/);
    expect(await completions(page)).toHaveLength(1);
  });

  test('a failed token exchange is shown and the step can be started again', async ({ page }) => {
    await useScenario(page, { connections: { google: 'exchange' } });
    await openGoogle(page);
    await consent(page, 'Calendar');
    const setup = page.locator('#google-setup');
    await setup.getByRole('button', { name: 'Finish connecting' }).click();
    await expect(setup.getByText('Google token exchange failed; start again')).toBeVisible();
    await expect(setup.getByRole('button', { name: 'Continue to Google' })).not.toHaveAttribute('aria-disabled');
  });
});

test.describe('Google setup without atrium', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('finishing is refused without sending and stays available', async ({ page }) => {
    await openGoogle(page, unknownReturn);
    await goUnreachable(page);
    const setup = page.locator('#google-setup');
    await setup.getByRole('button', { name: 'Finish connecting' }).click();
    await expect(setup.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    await expect(setup.getByRole('button', { name: 'Finish connecting' })).toBeVisible();
    expect(await completions(page)).toHaveLength(0);
  });

  test('a lost answer is shown as unknown and never sent again', async ({ page }) => {
    await useScenario(page, { connections: { google: 'lost' } });
    await openGoogle(page);
    await consent(page, 'Gmail');
    const setup = page.locator('#google-setup');
    await setup.getByRole('button', { name: 'Finish connecting' }).click();
    await expect(setup.getByText('The outcome is unknown')).toBeVisible();
    await expect(setup.getByRole('button', { name: 'Finish connecting' })).toHaveCount(0);
    await reconnect(page, /\/web\/v1\/connections$/);
    await expect(setup.getByRole('radio', { name: 'Gmail' })).toHaveAccessibleDescription(/^Connected /);
    expect(await completions(page)).toHaveLength(1);
  });
});

test.describe('Google setup design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      async function checked(page: Page): Promise<void> {
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await expectNoSeriousAxeFindings(page);
      }

      test('the steps fit, round, stay off blue and pass axe', async ({ page }) => {
        await useScenario(page, { connections: { google: 'fresh' } });
        await openGoogle(page);
        await expect(page.getByRole('heading', { name: 'Client details' })).toBeVisible();
        await checked(page);
      });

      test('the return and its result fit, round, stay off blue and pass axe', async ({ page }) => {
        await openGoogle(page);
        await consent(page, 'Gmail');
        const setup = page.locator('#google-setup');
        await expect(setup.getByRole('button', { name: 'Finish connecting' })).toBeVisible();
        await checked(page);
        await setup.getByRole('button', { name: 'Finish connecting' }).click();
        await expect(setup.getByText('Connected Gmail')).toBeVisible();
        await checked(page);
      });

      test('a denied consent fits, rounds, stays off blue and passes axe', async ({ page }) => {
        await useScenario(page, { connections: { google: 'denied' } });
        await openGoogle(page);
        await consent(page, 'Calendar');
        await expect(page.getByText('Google did not give consent')).toBeVisible();
        await checked(page);
      });
    });
  }
});
