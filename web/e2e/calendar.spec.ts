import type { Page } from '@playwright/test';
import type { CalendarRequest } from '../src/mocks/calendar.ts';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, storedKeys, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet'];

function writes(page: Page): Promise<CalendarRequest[]> {
  return page.evaluate(() => Reflect.get(globalThis, 'atriumCalendarRequests') ?? []);
}

function toasts(page: Page) {
  return page.getByRole('list', { name: 'Notifications' });
}

function row(page: Page, name: string | RegExp) {
  return page.getByRole('main').getByRole('button', { name });
}

function item(page: Page, name: string) {
  return page.getByRole('main').getByRole('listitem').filter({ hasText: name });
}

async function openAgenda(page: Page, path = '/apps/calendar'): Promise<void> {
  await gotoShell(page, path);
  await expect(page.getByRole('heading', { level: 1, name: 'Calendar' })).toBeVisible();
  await expect(row(page, /Studio standup/)).toBeVisible();
}

test.describe('calendar on a desktop', () => {
  test.use({ viewport: { width: 1440, height: 900 }, timezoneId: 'Pacific/Honolulu' });

  test('the agenda shows server times, all-day events, overlaps and calendars', async ({ page }) => {
    await openAgenda(page);
    const nav = page.getByRole('navigation', { name: 'Calendar' });
    await expect(nav.getByRole('link', { name: 'Agenda' })).toHaveAttribute('aria-current', 'page');
    await expect(row(page, /Studio standup/)).toContainText('09:30');
    await expect(row(page, /Studio standup/)).toContainText('Studio · Home');
    await expect(row(page, /Garden club fair/)).toContainText('All day');
    await expect(item(page, 'Lunch with Joris')).toContainText('Overlaps');
    await expect(item(page, 'Call with Sanne')).toContainText('Overlaps');
    await expect(item(page, 'Train to Rotterdam')).not.toContainText('Overlaps');
    await expect(row(page, /Truus Vos/)).toContainText('Birthdays');
    await expect(page.getByRole('heading', { name: /Today/ })).toHaveCount(1);
  });

  test('the week grid steps between weeks and back', async ({ page }) => {
    await gotoShell(page, '/apps/calendar/week');
    const days = page.getByRole('list', { name: 'Days' });
    await expect(days.getByRole('listitem').filter({ has: page.getByRole('heading', { level: 3 }) })).toHaveCount(7);
    await expect(row(page, /Studio standup/)).toContainText('09:30');
    const heading = page.getByRole('heading', { level: 2, name: /^Week of/ });
    const first = await heading.textContent();
    const thisWeek = page.getByRole('button', { name: 'This week' });
    await expect(thisWeek).toBeDisabled();
    await page.getByRole('button', { name: 'Next week' }).click();
    await expect(heading).not.toHaveText(first ?? '');
    await expect(row(page, /Dentist check-up/)).toBeVisible();
    await expect(row(page, /Studio standup/)).toHaveCount(0);
    await page.getByRole('link', { name: 'Agenda' }).click();
    await expect(page).toHaveURL(/\/apps\/calendar$/);
    await expect(row(page, /Dentist check-up/)).toBeVisible();
    await thisWeek.click();
    await expect(heading).toHaveText(first ?? '');
    await expect(thisWeek).toBeDisabled();
  });

  test('a new event is added with one key and can be undone', async ({ page }) => {
    await openAgenda(page);
    await page.getByRole('button', { name: 'New event' }).click();
    const dialog = page.getByRole('dialog', { name: 'New event' });
    await dialog.getByRole('button', { name: 'Add event' }).click();
    await expect(dialog.getByText('Give the event a title.')).toBeVisible();
    expect(await writes(page)).toEqual([]);
    await dialog.getByLabel('Title').fill('Bike repair');
    await dialog.getByLabel('Location').fill('Wheel Works');
    await dialog.getByRole('button', { name: 'Add event' }).click();
    await expect(dialog).toBeHidden();
    await expect(toasts(page)).toContainText('Added Bike repair.');
    await expect(row(page, /Bike repair/)).toContainText('09:00');
    const sent = await writes(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ method: 'POST', path: '/calendar/events' });
    expect(sent[0]?.key).toBeTruthy();
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(toasts(page)).toContainText('Undid adding Bike repair.');
    await expect(row(page, /Bike repair/)).toHaveCount(0);
  });

  test('an edit sends the revision it read and can be undone', async ({ page }) => {
    await openAgenda(page);
    await row(page, /Train to Rotterdam/).click();
    const dialog = page.getByRole('dialog', { name: 'Train to Rotterdam' });
    await expect(dialog).toContainText('08:15');
    await dialog.getByRole('button', { name: 'Edit' }).click();
    const form = page.getByRole('dialog', { name: 'Edit event' });
    await expect(form.getByLabel('Title')).toHaveValue('Train to Rotterdam');
    await form.getByLabel('Title').fill('Train to Utrecht');
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form).toBeHidden();
    await expect(toasts(page)).toContainText('Saved Train to Utrecht.');
    await expect(row(page, /Train to Utrecht/)).toBeVisible();
    const sent = await writes(page);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({ method: 'PATCH', path: '/calendar/events/icloud/evt-train', ifMatch: 'W/"1"' });
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(row(page, /Train to Rotterdam/)).toBeVisible();
    await expect(row(page, /Train to Utrecht/)).toHaveCount(0);
  });

  test('a delete asks first and can be undone', async ({ page }) => {
    await openAgenda(page);
    await row(page, /Dinner with Ilse/).click();
    await page.getByRole('dialog', { name: 'Dinner with Ilse' }).getByRole('button', { name: 'Delete' }).click();
    const confirm = page.getByRole('dialog', { name: 'Delete this event?' });
    await confirm.getByRole('button', { name: 'Keep it' }).click();
    await expect(page.getByRole('dialog', { name: 'Dinner with Ilse' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Dinner with Ilse' }).getByRole('button', { name: 'Delete' }).click();
    await confirm.getByRole('button', { name: 'Delete' }).click();
    await expect(confirm).toBeHidden();
    await expect(toasts(page)).toContainText('Deleted Dinner with Ilse.');
    await expect(row(page, /Dinner with Ilse/)).toHaveCount(0);
    expect(await writes(page)).toEqual([expect.objectContaining({ method: 'DELETE', ifMatch: 'W/"1"' })]);
    await toasts(page).getByRole('button', { name: 'Undo' }).click();
    await expect(row(page, /Dinner with Ilse/)).toBeVisible();
  });

  test('birthdays are read-only', async ({ page }) => {
    await openAgenda(page);
    await row(page, /Truus Vos/).click();
    const dialog = page.getByRole('dialog', { name: 'Truus Vos, turns 88' });
    await expect(dialog).toContainText('Birthdays, read-only');
    await expect(dialog).toContainText('All day');
    await expect(dialog.getByRole('button', { name: 'Edit' })).toHaveCount(0);
    await expect(dialog.getByRole('button', { name: 'Delete' })).toHaveCount(0);
  });

  test.describe('refused writes', () => {
    test.use({ allowStatus: [409, 412] });

    test('a stale edit reloads the event and says so', async ({ page }) => {
      await useScenario(page, { calendar: { stale: true } });
      await openAgenda(page);
      await row(page, /Studio standup/).click();
      await page.getByRole('dialog', { name: 'Studio standup' }).getByRole('button', { name: 'Edit' }).click();
      const form = page.getByRole('dialog', { name: 'Edit event' });
      await form.getByLabel('Location').fill('Back room');
      await form.getByRole('button', { name: 'Save' }).click();
      await expect(form).toContainText('This event changed since it loaded');
      await expect(form.getByLabel('Title')).toHaveValue('Studio standup (moved)');
      await expect(form.getByLabel('Location')).toHaveValue('Studio');
      await expect(row(page, /Studio standup \(moved\)/)).toBeVisible();
      await form.getByLabel('Location').fill('Back room');
      await form.getByRole('button', { name: 'Save' }).click();
      await expect(toasts(page)).toContainText('Saved Studio standup (moved).');
      expect((await writes(page)).map((item) => item.ifMatch)).toEqual(['W/"1"', 'W/"2"']);
    });

    test('an unknown outcome locks the event and points to the inbox', async ({ page }) => {
      await useScenario(page, { calendar: { unknown: true } });
      await openAgenda(page);
      await row(page, /Studio standup/).click();
      await page.getByRole('dialog', { name: 'Studio standup' }).getByRole('button', { name: 'Edit' }).click();
      const form = page.getByRole('dialog', { name: 'Edit event' });
      await form.getByLabel('Title').fill('Standup moved');
      await form.getByRole('button', { name: 'Save' }).click();
      await expect(form).toContainText('The outcome is unknown');
      await expect(form.getByRole('link', { name: 'Open the inbox' })).toBeVisible();
      await expect(form.getByRole('button', { name: 'Save' })).toHaveAttribute('aria-disabled', 'true');
      await form.getByRole('button', { name: 'Cancel' }).click();
      await page.keyboard.press('Escape');
      await row(page, /Studio standup/).click();
      const view = page.getByRole('dialog', { name: 'Studio standup' });
      await expect(view).toContainText('The outcome is unknown');
      await expect(view.getByRole('button', { name: 'Edit' })).toHaveAttribute('aria-disabled', 'true');
      await expect(view.getByRole('button', { name: 'Delete' })).toBeDisabled();
      expect(await writes(page)).toHaveLength(1);
    });
  });

  test('the editor works from the keyboard alone', async ({ page }) => {
    await openAgenda(page);
    await page.getByRole('button', { name: 'New event' }).focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'New event' });
    await expect(dialog).toBeVisible();
    const title = dialog.getByLabel('Title');
    for (let step = 0; step < 4; step += 1) {
      if (await title.evaluate((node) => node === document.activeElement)) break;
      await page.keyboard.press('Tab');
    }
    await expect(title).toBeFocused();
    await page.keyboard.type('Piano lesson');
    await page.keyboard.press('Enter');
    await expect(dialog).toBeHidden();
    await expect(toasts(page)).toContainText('Added Piano lesson.');
    await expect(page.getByRole('button', { name: 'New event' })).toBeFocused();
  });

  test('candidates list calendar proposals that open in the inbox', async ({ page }) => {
    await gotoShell(page, '/apps/calendar/candidates');
    const link = page.getByRole('link', { name: /Move the dentist visit to Friday/ });
    await expect(link).toBeVisible();
    await expect(page.getByRole('main').getByRole('listitem')).toHaveCount(1);
    await link.click();
    await expect(page).toHaveURL(/\/inbox\/proposal(:|%3A)12$/);
  });

  test('candidates say when nothing is proposed', async ({ page }) => {
    await useScenario(page, { inbox: { empty: true } });
    await gotoShell(page, '/apps/calendar/candidates');
    await expect(page.getByRole('heading', { name: 'No proposed events' })).toBeVisible();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [503] });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { calendar: { error: true } });
      await gotoShell(page, '/apps/calendar');
      await expect(page.getByRole('heading', { name: 'The calendar did not load' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
    });
  });

  test('an empty week says so', async ({ page }) => {
    await useScenario(page, { calendar: { empty: true } });
    await gotoShell(page, '/apps/calendar');
    await expect(page.getByRole('heading', { name: 'Nothing this week' })).toBeVisible();
  });

  test.describe('offline', () => {
    test.use({ allowConsole: unreachableConsole });

    test('offline blocks writes and the device keeps no calendar data', async ({ page }) => {
      await openAgenda(page);
      await goUnreachable(page);
      await expect(page.getByRole('button', { name: 'New event' })).toHaveAttribute('aria-disabled', 'true');
      await page.getByRole('button', { name: 'Next week' }).click();
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' })).toBeVisible();
      expect((await storedKeys(page)).filter((key) => key.includes('calendar'))).toEqual([]);
    });
  });

  test('the hub card and the rail open the calendar pages', async ({ page }) => {
    await gotoShell(page, '/apps');
    const card = page.locator('[data-app="calendar"]');
    await expect(card.getByRole('link', { name: 'Agenda' })).toBeVisible();
    await expect(card.getByRole('link', { name: 'Candidates' })).toBeVisible();
    await card.getByRole('link', { name: 'Week' }).click();
    await expect(page).toHaveURL(/\/apps\/calendar\/week$/);
    await expect(page.getByRole('navigation', { name: 'Calendar' }).getByRole('link', { name: 'Week' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    await expect(page.locator('.rail').getByRole('link', { name: 'Calendar' })).toHaveAttribute('aria-current', 'page');
  });
});

test.describe('calendar on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('the phone shows the agenda and the editor as a sheet', async ({ page }) => {
    await openAgenda(page);
    const nav = page.getByRole('navigation', { name: 'Calendar' });
    await expect(nav.getByRole('link', { name: 'Week' })).toBeHidden();
    await row(page, /Studio standup/).click();
    await expect(page.locator('dialog.sheet')).toBeVisible();
  });
});

for (const layout of layouts) {
  test.describe(`calendar at ${layout.name}`, () => {
    test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

    for (const path of ['/apps/calendar', '/apps/calendar/week', '/apps/calendar/candidates']) {
      test(`${path} fits the ${layout.name} layout`, async ({ page }) => {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { level: 1, name: 'Calendar' })).toBeVisible();
        await expect(page.getByRole('main').getByRole('listitem').first()).toBeVisible();
        await expectTouchTargets(page, layout.touch ? 'body' : 'main');
        await expectRoundedSurfaces(page, edges);
        await expectNoHorizontalScroll(page);
        await expectNoSeriousAxeFindings(page);
      });
    }

    test(`the editor fits the ${layout.name} layout`, async ({ page }) => {
      await openAgenda(page);
      await row(page, /Call with Sanne/).click();
      await page.getByRole('button', { name: 'Edit' }).click();
      await expect(page.getByRole('dialog', { name: 'Edit event' }).getByLabel('Notes')).toHaveValue(
        'Bring the three quotes.',
      );
      if (layout.touch) await expectTouchTargets(page, 'dialog');
      await expectRoundedSurfaces(page, edges);
      await expectNoHorizontalScroll(page);
      await expectNoSeriousAxeFindings(page);
    });
  });
}
