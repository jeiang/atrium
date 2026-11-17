import type { Page } from '@playwright/test';
import type { NotificationsRequest } from '../src/mocks/notifications.ts';
import { expectNoHorizontalScroll, expectNoSeriousAxeFindings, expectTouchTargets, gotoShell } from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

type Engine = 'granted' | 'default' | 'denied' | 'unsupported' | 'install';

interface PushLog {
  prompts: number;
  subscribes: number;
  endpoints: string[];
}

const ENDPOINT = 'https://push.example.test/send/this-browser';

async function fakePush(page: Page, engine: Engine, allow = true): Promise<void> {
  await page.addInitScript(
    ({ engine, allow, endpoint }) => {
      const log = { prompts: 0, subscribes: 0, endpoints: [] as string[] };
      Object.assign(globalThis, { atriumPush: log });
      let permission: NotificationPermission = engine === 'granted' || engine === 'denied' ? engine : 'default';
      const request = async (): Promise<NotificationPermission> => {
        log.prompts++;
        if (permission === 'default') permission = allow ? 'granted' : 'denied';
        return permission;
      };
      if ('Notification' in globalThis) {
        Object.defineProperty(Notification, 'permission', { configurable: true, get: () => permission });
        Object.defineProperty(Notification, 'requestPermission', { configurable: true, value: request });
      } else {
        Object.defineProperty(globalThis, 'Notification', {
          configurable: true,
          value: {
            get permission() {
              return permission;
            },
            requestPermission: request,
          },
        });
      }
      const proto = globalThis.ServiceWorkerRegistration?.prototype;
      if (engine === 'unsupported' || engine === 'install') {
        if (proto) Reflect.deleteProperty(proto, 'pushManager');
        Reflect.deleteProperty(globalThis, 'PushManager');
        if (engine === 'install') Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
        return;
      }
      if (!('PushManager' in globalThis)) Object.defineProperty(globalThis, 'PushManager', { value: class {} });
      const bytes = (length: number, first: number) => {
        const out = new Uint8Array(length).map((_, index) => (index * 7 + 3) % 256);
        out[0] = first;
        return out.buffer;
      };
      const managers = new Map<string, unknown>();
      const manager = (scope: string) => {
        let current: PushSubscription | null = null;
        return {
          getSubscription: async () => current,
          permissionState: async () => (permission === 'default' ? 'prompt' : permission),
          subscribe: async (options: PushSubscriptionOptionsInit) => {
            if (permission === 'default') await request();
            if (permission !== 'granted') throw new DOMException('Permission denied', 'NotAllowedError');
            log.subscribes++;
            log.endpoints.push(endpoint);
            const key = options.applicationServerKey as Uint8Array;
            const made = {
              endpoint,
              expirationTime: null,
              options: { userVisibleOnly: true, applicationServerKey: key.buffer },
              getKey: (name: string) => (name === 'p256dh' ? bytes(65, 4) : bytes(16, 1)),
              toJSON: () => ({ endpoint }),
              unsubscribe: async () => {
                current = null;
                log.endpoints = log.endpoints.filter((item) => item !== endpoint);
                return true;
              },
            };
            current = made as unknown as PushSubscription;
            return current;
          },
          scope,
        };
      };
      if (proto) {
        Object.defineProperty(proto, 'pushManager', {
          configurable: true,
          get(this: ServiceWorkerRegistration) {
            if (!managers.has(this.scope)) managers.set(this.scope, manager(this.scope));
            return managers.get(this.scope);
          },
        });
      }
    },
    { engine, allow, endpoint: ENDPOINT },
  );
}

async function pushLog(page: Page): Promise<PushLog> {
  return page.evaluate(() => (globalThis as unknown as { atriumPush: PushLog }).atriumPush);
}

async function requests(page: Page): Promise<NotificationsRequest[]> {
  return page.evaluate(
    () => (globalThis as { atriumNotifyRequests?: NotificationsRequest[] }).atriumNotifyRequests ?? [],
  );
}

const pushSwitch = (page: Page) => page.getByRole('switch', { name: /Push on this device/ });
const deviceList = (page: Page) => page.getByRole('list', { name: 'Devices with push' });
const notice = (page: Page, text: string | RegExp) => page.locator('.notice').filter({ hasText: text });

async function openPage(page: Page): Promise<void> {
  await gotoShell(page, '/settings/notifications');
  await expect(page.getByRole('heading', { name: 'Notifications', level: 2 })).toBeVisible();
  await expect(pushSwitch(page)).toBeVisible();
}

test.describe('notifications on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409] });

  test('push turns on, sends a test and turns off without a prompt on load', async ({ page }) => {
    await fakePush(page, 'default');
    await openPage(page);
    await expect(pushSwitch(page)).not.toBeChecked();
    expect((await pushLog(page)).prompts).toBe(0);
    expect(await requests(page)).toEqual([]);

    await pushSwitch(page).click();
    await expect(notice(page, 'Push is on for this device')).toBeVisible();
    await expect(pushSwitch(page)).toBeChecked();
    const mine = deviceList(page).getByRole('listitem').filter({ hasText: 'This device' });
    await expect(mine).toHaveCount(1);
    expect((await pushLog(page)).prompts).toBe(1);
    const [registered] = await requests(page);
    expect(registered?.method).toBe('POST');
    expect(registered?.path).toMatch(/\/devices$/);
    expect(registered?.key).toBeTruthy();
    expect(registered?.body).toMatchObject({ endpoint: ENDPOINT, auth: expect.any(String), p256dh: expect.any(String) });

    await page.getByRole('button', { name: 'Send a test push' }).click();
    await expect(notice(page, /Test sent to 3 devices at/)).toBeVisible();

    await pushSwitch(page).click();
    await expect(notice(page, 'Push is off for this device')).toBeVisible();
    await expect(pushSwitch(page)).not.toBeChecked();
    await expect(deviceList(page).getByText('This device')).toHaveCount(0);
    expect((await pushLog(page)).endpoints).toEqual([]);
    const sent = await requests(page);
    expect(sent.map((item) => item.method)).toEqual(['POST', 'POST', 'DELETE']);
    expect(new Set(sent.map((item) => item.key)).size).toBe(3);
  });

  test('removing a device names it and starts on Cancel', async ({ page }) => {
    await fakePush(page, 'granted');
    await openPage(page);
    await page.getByRole('button', { name: 'Remove Safari on iOS' }).click();
    const dialog = page.getByRole('dialog', { name: 'Remove Safari on iOS?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Remove device' }).click();
    await expect(dialog).toBeHidden();
    await expect(deviceList(page).getByText('Safari on iOS')).toHaveCount(0);
    await expect(deviceList(page).getByRole('listitem').filter({ hasText: 'Firefox on Linux' })).toHaveCount(1);
    const [removed] = await requests(page);
    expect(removed?.method).toBe('DELETE');
    expect(removed?.path).toMatch(/\/devices\/2$/);
  });

  test('a failing device is marked', async ({ page }) => {
    await fakePush(page, 'granted');
    await openPage(page);
    const row = deviceList(page).getByRole('listitem').filter({ hasText: 'Safari on iOS' });
    await expect(row.getByText('Failing')).toBeVisible();
    await expect(row.getByText(/last attempt failed: push service answered 410 Gone/)).toBeVisible();
  });

  test('channel content saves only what changed', async ({ page }) => {
    await fakePush(page, 'granted');
    await openPage(page);
    const save = page.getByRole('button', { name: 'Save' });
    await expect(save).toBeDisabled();
    await page.getByLabel('System').selectOption('full');
    await save.click();
    await expect(notice(page, /Saved at/)).toBeVisible();
    await expect(page.getByLabel('System')).toHaveValue('full');
    await expect(save).toBeDisabled();
    const sent = await requests(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ method: 'PUT', body: { content: 'full' } });
    expect(sent[0]?.path).toMatch(/\/notifications\/channels\/system$/);
  });

  test('a refused permission prompt leaves push off', async ({ page }) => {
    await fakePush(page, 'default', false);
    await openPage(page);
    await pushSwitch(page).click();
    await expect(page.getByText('Notifications are blocked for this site', { exact: true })).toBeVisible();
    await expect(pushSwitch(page)).not.toBeChecked();
    await expect(pushSwitch(page)).toHaveAttribute('aria-disabled', 'true');
    expect(await requests(page)).toEqual([]);
  });

  test('a test with no devices is not offered', async ({ page }) => {
    await fakePush(page, 'granted');
    await useScenario(page, { notifications: { devices: 'none' } });
    await openPage(page);
    await expect(page.getByText('No device gets push')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Send a test push' })).toBeDisabled();
  });

  test('a test the push service refuses says why', async ({ page }) => {
    await fakePush(page, 'granted');
    await useScenario(page, { notifications: { testFails: true } });
    await openPage(page);
    await page.getByRole('button', { name: 'Send a test push' }).click();
    await expect(page.getByText('Test not delivered: push service answered 410 Gone')).toBeVisible();
  });

  for (const [engine, text] of [
    ['denied', 'Notifications are blocked for this site. Allow them'],
    ['unsupported', 'This browser cannot receive push.'],
    ['install', 'add it there with Share'],
  ] as const) {
    test(`a ${engine} browser says so and offers no switch`, async ({ page }) => {
      await fakePush(page, engine);
      await openPage(page);
      await expect(page.getByText(text, { exact: false })).toBeVisible();
      await expect(pushSwitch(page)).toHaveAttribute('aria-disabled', 'true');
      expect(await requests(page)).toEqual([]);
      expect((await pushLog(page)).prompts).toBe(0);
    });
  }

  test('a server without a push key says what is missing', async ({ page }) => {
    await fakePush(page, 'granted');
    await useScenario(page, { notifications: { key: 'missing' } });
    await openPage(page);
    await expect(page.getByText('Push is not set up on the server: webpush_subject is not set.')).toBeVisible();
    await expect(pushSwitch(page)).toHaveAttribute('aria-disabled', 'true');
  });
});

test.describe('notifications failures', () => {
  test.use({
    viewport: { width: 1440, height: 900 },
    allowStatus: [500],
    allowConsole: unreachableConsole,
  });

  test('a load error offers a retry', async ({ page }) => {
    await fakePush(page, 'granted');
    await useScenario(page, { notifications: { fail: true } });
    await gotoShell(page, '/settings/notifications');
    await expect(page.getByRole('heading', { name: 'The notification settings did not load' })).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
  });

  test('a lost registration is reported once and not retried', async ({ page }) => {
    await fakePush(page, 'granted');
    await useScenario(page, { notifications: { subscribeFails: 'lost' } });
    await openPage(page);
    await pushSwitch(page).click();
    await expect(page.getByText('The outcome is unknown')).toBeVisible();
    await expect(pushSwitch(page)).not.toBeChecked();
    expect((await requests(page)).filter((item) => item.method === 'POST')).toHaveLength(1);
  });

  test('offline turns are refused at once', async ({ page }) => {
    await fakePush(page, 'default');
    await openPage(page);
    await goUnreachable(page);
    await pushSwitch(page).click();
    await expect(page.getByText('Cannot reach atrium', { exact: true }).last()).toBeVisible();
    await expect(pushSwitch(page)).not.toBeChecked();
    await page.getByRole('button', { name: 'Send a test push' }).click();
    expect(await requests(page)).toEqual([]);
    expect((await pushLog(page)).prompts).toBe(0);
  });
});

test.describe('notifications design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      async function checks(page: Page, scope = 'body'): Promise<void> {
        await expectNoHorizontalScroll(page);
        if (layout.touch) await expectTouchTargets(page, scope);
        await expectNoSeriousAxeFindings(page);
      }

      test('the page with push on passes checks', async ({ page }) => {
        await fakePush(page, 'granted');
        await openPage(page);
        await pushSwitch(page).click();
        await expect(pushSwitch(page)).toBeChecked();
        await expect(deviceList(page).getByText('This device')).toBeVisible();
        if (layout.width < 861) await expect(page.getByRole('link', { name: 'All settings' })).toBeVisible();
        await checks(page);
      });

      test('the empty, unset page passes checks', async ({ page }) => {
        await fakePush(page, 'install');
        await useScenario(page, { notifications: { devices: 'none', key: 'missing' } });
        await openPage(page);
        await expect(page.getByText('No device gets push')).toBeVisible();
        await checks(page);
      });

      test('the remove confirmation passes checks', async ({ page }) => {
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await fakePush(page, 'granted');
        await openPage(page);
        await page.getByRole('button', { name: 'Remove Firefox on Linux' }).click();
        await expect(page.getByRole('dialog', { name: 'Remove Firefox on Linux?' })).toBeVisible();
        await checks(page, 'dialog[open]');
      });
    });
  }
});
