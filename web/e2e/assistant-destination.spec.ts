import type { Page } from '@playwright/test';
import type { AssistantRequest } from '../src/mocks/assistant.ts';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

function requests(page: Page): Promise<AssistantRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumAssistantRequests') ?? []);
}

async function openSchedule(page: Page, id: number, name: string): Promise<void> {
  await gotoShell(page, `/assistant/routines/${id}`);
  await expect(page.getByRole('heading', { level: 2, name })).toBeVisible();
}

test.describe('the Assistant destination', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409, 412, 422, 503] });

  test('the sub-navigation reaches Routines, Authority, Spend and Behavior', async ({ page }) => {
    await gotoShell(page, '/assistant');
    await expect(page.getByRole('heading', { level: 1, name: 'Routines' })).toBeVisible();
    const nav = page.getByRole('navigation', { name: 'Assistant' });
    for (const [name, heading] of [
      ['Authority', 'Authority'],
      ['Spend', 'Spend'],
      ['Behavior', 'Behavior'],
      ['Routines', 'Routines'],
    ]) {
      await nav.getByRole('link', { name }).click();
      await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
      await expect(nav.getByRole('link', { name })).toHaveAttribute('aria-current', 'page');
    }
  });

  test('Behavior saves the defaults for new chats with one key per press', async ({ page }) => {
    await gotoShell(page, '/assistant/behavior');
    const form = page.locator('[data-settings="defaults"]');
    await form.getByLabel('Model').selectOption('claude-opus-5-5');
    await form.getByLabel('Effort').selectOption('max');
    await form.getByRole('button', { name: 'Save defaults' }).click();
    await expect(form.getByText('Defaults saved')).toBeVisible();
    const sent = (await requests(page)).filter((item) => item.path.endsWith('/assistant/defaults'));
    expect(sent).toHaveLength(1);
    expect(sent[0]?.idempotencyKey).toMatch(uuid);
    expect(sent[0]?.body).toMatchObject({ model: 'claude-opus-5-5', effort: 'max' });
    await expect(page.getByRole('list', { name: 'Research roles' })).toContainText('researcher');
  });

  test('the routine settings list the schedule keys atrium offers', async ({ page }) => {
    await gotoShell(page, '/assistant');
    const form = page.locator('form#routine-settings');
    for (const key of ['digest_notify', 'agent_schedule_min_interval_seconds']) {
      await expect(form.locator(`[data-key="${key}"]`)).toBeVisible();
    }
  });

  test('a helper setting saves through the shared settings form', async ({ page }) => {
    await gotoShell(page, '/assistant/behavior');
    const form = page.locator('form#helpers');
    await form.locator('[data-key="search_web_model"]').getByRole('radio', { name: 'claude-sonnet-5-5' }).check();
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form.getByText(/^Saved at/)).toBeVisible();
  });

  test('a stale spending limit keeps the draft and says it changed elsewhere', async ({ page }) => {
    await useScenario(page, { settings: { elsewhere: { thread_cost_cap_usd: '12' } } });
    await gotoShell(page, '/assistant/spend');
    const form = page.locator('form#caps');
    const budget = form.locator('[data-key="monthly_budget_usd"]').getByRole('textbox');
    await budget.fill('180');
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form.getByText('The settings changed elsewhere')).toBeVisible();
    await expect(budget).toHaveValue('180');
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form.getByText(/^Saved at/)).toBeVisible();
  });

  test('Spend shows the month, a readable breakdown table and the largest runs', async ({ page }) => {
    await gotoShell(page, '/assistant/spend');
    await expect(page.locator('.asst-meter[data-state="ok"]')).toContainText('$42.18');
    const table = page.getByRole('table', { name: 'Spending by kind' });
    await expect(table.getByRole('columnheader', { name: 'Cost' })).toBeVisible();
    await expect(table.locator('tbody tr')).toHaveCount(4);
    await page.getByRole('radio', { name: 'By model' }).check();
    await expect(page.getByRole('table', { name: 'Spending by model' }).locator('tbody tr')).toHaveCount(2);
    await expect(page.locator('#runs')).toContainText('Plan the allotment rota for spring');
  });

  for (const state of ['near', 'exhausted'] as const) {
    test(`a ${state} budget says what atrium refuses`, async ({ page }) => {
      await useScenario(page, { assistant: { spend: state } });
      await gotoShell(page, '/assistant/spend');
      await expect(page.locator(`.asst-meter[data-state="${state}"]`)).toBeVisible();
      await expect(page.getByText(state === 'exhausted' ? 'The budget is used up' : /are refused until/)).toBeVisible();
    });
  }

  test('Authority saves a permission and shows its requests', async ({ page }) => {
    await gotoShell(page, '/assistant/authority');
    await expect(page.locator('[data-own="at-once"]')).toBeVisible();
    const internet = page.locator('[data-key="permission_internet_clean"]');
    await internet.getByRole('combobox').selectOption('ask');
    await page.getByRole('button', { name: 'Save authority' }).click();
    await expect(page.getByText('Authority saved')).toBeVisible();
    const sent = (await requests(page)).filter((item) => item.path.endsWith('/permissions'));
    expect(sent).toHaveLength(1);
    expect(sent[0]?.idempotencyKey).toMatch(uuid);
    expect(sent[0]?.body).toEqual({ changes: [{ key: 'permission_internet_clean', value: 'ask' }] });
    const restricted = page.locator('[data-key="permission_people_untrusted_knowledge"]').getByRole('combobox');
    await expect(restricted.locator('option')).toHaveText(['Ask me first', 'Refuse']);
    await expect(page.getByRole('list', { name: 'Recent permission requests' })).toContainText(
      'Fetch a page from parcels.example.test',
    );
  });

  test('a created schedule appears with its next run after one confirmed request', async ({ page }) => {
    await gotoShell(page, '/assistant/routines');
    await page.getByRole('button', { name: 'New schedule' }).click();
    const dialog = page.locator('dialog[open]');
    await dialog.getByLabel('Name').fill('Seed order reminder');
    await dialog.getByLabel('When').fill('every day');
    await dialog.getByLabel('What it does').fill('Remind me to order seeds.');
    await dialog.getByRole('button', { name: 'Review' }).click();
    await dialog.getByRole('button', { name: 'Set up schedule' }).click();
    await expect(dialog.getByText(/unrecognised schedule/).first()).toBeVisible();
    await dialog.getByLabel('When').fill('weekly sat 08:30');
    await dialog.getByRole('button', { name: 'Review' }).click();
    await expect(page.getByRole('heading', { name: 'Set up Seed order reminder?' })).toBeVisible();
    await dialog.getByRole('button', { name: 'Set up schedule' }).click();
    const list = page.getByRole('list', { name: 'Schedules' });
    await expect(list).toContainText('Seed order reminder');
    await expect(list).toContainText('Every sat at 08:30');
    const sent = (await requests(page)).filter((item) => item.method === 'POST' && item.path.endsWith('/schedules'));
    expect(sent).toHaveLength(2);
    expect(new Set(sent.map((item) => item.idempotencyKey)).size).toBe(2);
  });

  test('Run now starts a run with its own key, and says when it is already running', async ({ page }) => {
    await openSchedule(page, 1, 'Morning brief');
    await page.getByRole('button', { name: 'Run now' }).click();
    await expect(page.getByText(/^Started run \d+/)).toBeVisible();
    const first = (await requests(page)).filter((item) => item.path.endsWith('/run'));
    expect(first).toHaveLength(1);
    expect(first[0]?.idempotencyKey).toMatch(uuid);
    await openSchedule(page, 3, 'Garden club newsletter');
    await page.getByRole('button', { name: 'Run now' }).click();
    await expect(page.getByText('Already running', { exact: true })).toBeVisible();
    await expect(page.getByText('Garden club newsletter is already running (run 141)')).toBeVisible();
    const second = (await requests(page)).filter((item) => item.path.endsWith('/run'));
    expect(second).toHaveLength(1);
    expect(second[0]?.idempotencyKey).not.toBe(first[0]?.idempotencyKey);
  });

  test('Run now says when atrium serve is not running', async ({ page }) => {
    await useScenario(page, { assistant: { runNow: 'down' } });
    await openSchedule(page, 1, 'Morning brief');
    await page.getByRole('button', { name: 'Run now' }).click();
    await expect(page.getByText(/atrium serve is not running on this database/)).toBeVisible();
  });

  test('Run now says when atrium refuses to start it', async ({ page }) => {
    await useScenario(page, { assistant: { runNow: 'refused' } });
    await openSchedule(page, 1, 'Morning brief');
    await page.getByRole('button', { name: 'Run now' }).click();
    await expect(page.getByText('atrium refused to run it')).toBeVisible();
    await expect(page.getByText('the assistant app is off. Nothing was started.')).toBeVisible();
  });

  test('a protected schedule cannot be turned off', async ({ page }) => {
    await openSchedule(page, 1, 'Morning brief');
    await expect(page.getByRole('button', { name: 'Turn off' })).toBeDisabled();
    await expect(page.getByText(/This schedule is protected: atrium relies on it/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Delete schedule' })).toHaveCount(0);
  });

  test('a schedule changed elsewhere keeps the draft and reloads', async ({ page }) => {
    await useScenario(page, { assistant: { scheduleElsewhere: true } });
    await openSchedule(page, 3, 'Garden club newsletter');
    const when = page.locator('[data-settings="schedule"]').getByLabel('When');
    await when.fill('weekly tue 09:00');
    await page.getByRole('button', { name: 'Save schedule' }).click();
    await expect(page.getByText('This changed since it loaded')).toBeVisible();
    await expect(when).toHaveValue('weekly tue 09:00');
    const prompt = page.locator('[data-settings="schedule"]').getByLabel('What it does');
    await expect(prompt).toHaveValue(/Also list the plant sale dates\.$/);
    await page.getByRole('button', { name: 'Save schedule' }).click();
    await expect(page.getByText('Schedule saved')).toBeVisible();
    await expect(page.locator('[data-field="cadence"]')).toHaveText('Every tue at 09:00');
  });

  test('an empty atrium says so on every page', async ({ page }) => {
    await useScenario(page, { assistant: { empty: true } });
    await gotoShell(page, '/assistant/routines');
    await expect(page.getByRole('heading', { name: 'No schedules' })).toBeVisible();
    await gotoShell(page, '/assistant/spend');
    await expect(page.getByText('No paid run has happened this month.').first()).toBeVisible();
    await gotoShell(page, '/assistant/authority');
    await expect(page.getByRole('heading', { name: 'No requests yet' })).toBeVisible();
    await gotoShell(page, '/assistant/behavior');
    await expect(page.getByRole('heading', { name: 'No roles' })).toBeVisible();
  });

  test.describe('failed reads', () => {
    test.use({ allowStatus: [500] });
    for (const [fail, path, title] of [
      ['schedules', '/assistant/routines', 'Schedules did not load'],
      ['spend', '/assistant/spend', 'Spending did not load'],
      ['permissions', '/assistant/authority', 'The permissions did not load'],
      ['configuration', '/assistant/behavior', 'The defaults did not load'],
    ] as const) {
      test(`a failed ${fail} read offers a retry`, async ({ page }) => {
        await useScenario(page, { assistant: { fail } });
        await gotoShell(page, path);
        const state = page.getByRole('heading', { name: title });
        await expect(state).toBeVisible();
        await expect(page.getByRole('button', { name: 'Retry' }).first()).toBeVisible();
      });
    }
  });
});

test.describe('the Assistant destination offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('a cached schedule reads and Run now is refused without sending', async ({ page }) => {
    await openSchedule(page, 1, 'Morning brief');
    await expect(page.getByRole('list', { name: 'Run history' })).toBeVisible();
    await goUnreachable(page);
    await page.getByRole('button', { name: 'Run now' }).click();
    await expect(page.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    expect((await requests(page)).filter((item) => item.path.endsWith('/run'))).toHaveLength(0);
  });

  test('Authority refuses a save without sending', async ({ page }) => {
    await gotoShell(page, '/assistant/authority');
    await page.locator('[data-key="permission_internet_clean"]').getByRole('combobox').selectOption('deny');
    await goUnreachable(page);
    await page.getByRole('button', { name: 'Save authority' }).click();
    await expect(page.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
    expect((await requests(page)).filter((item) => item.path.endsWith('/permissions'))).toHaveLength(0);
  });
});

test.describe('the Assistant destination layout', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      for (const path of [
        '/assistant/routines',
        '/assistant/routines/1',
        '/assistant/authority',
        '/assistant/spend',
        '/assistant/behavior',
      ]) {
        test(`${path} fits, rounds and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(page.locator('.skeleton')).toHaveCount(0);
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      }

      test('the new schedule dialog passes axe', async ({ page }) => {
        await page.emulateMedia({ reducedMotion: 'reduce' });
        await gotoShell(page, '/assistant/routines');
        await page.getByRole('button', { name: 'New schedule' }).click();
        await expect(page.locator('dialog[open]')).toBeVisible();
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoSeriousAxeFindings(page);
      });
    });
  }
});
