import type { Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks';
import { expect, goUnreachable, layouts, reconnect, test, unreachableConsole, useScenario } from './fixtures';

interface Sent {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function sent(page: Page): Promise<Sent[]> {
  const all = await page.evaluate(
    () => (globalThis as typeof globalThis & { atriumKnowledgeRequests?: Sent[] }).atriumKnowledgeRequests ?? [],
  );
  return all.filter((item) => item.method !== 'GET');
}

function toast(page: Page, text: string | RegExp) {
  return page.locator('.toast__text', { hasText: text }).last();
}

async function retractIlse(page: Page): Promise<void> {
  await gotoShell(page, '/knowledge/21');
  await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
  await page.getByRole('button', { name: 'Retract' }).click();
  await expect(page.locator('.kpage').getByRole('button', { name: 'Delete for good' })).toBeVisible();
}

async function checkLayout(page: Page, touch: boolean): Promise<void> {
  await expectNoHorizontalScroll(page);
  await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
  if (touch) await expectTouchTargets(page, (await page.locator('dialog[open]').count()) > 0 ? 'dialog[open]' : 'body');
  await expectNoBlue(page);
  await expectNoSeriousAxeFindings(page);
}

test.describe('knowledge maintenance', () => {
  test('review confirms the selected records once with Undo', async ({ page }) => {
    await gotoShell(page, '/knowledge');
    await page
      .getByRole('navigation', { name: 'Knowledge subjects' })
      .getByRole('link', { name: /^Review/ })
      .click();
    await expect(page).toHaveURL(/\/knowledge\/review$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Review' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Waiting for review' });
    await expect(list.getByRole('listitem')).toHaveCount(5);
    const confirm = page.getByRole('button', { name: /^Confirm selected/ });
    await expect(confirm).toBeDisabled();
    await list.getByRole('checkbox', { name: 'Select Garden weather station' }).check();
    await list.getByRole('checkbox', { name: 'Select Shed roof needs four sheets' }).check();
    await expect(confirm).toHaveText('Confirm selected (2)');
    await confirm.click();
    await expect(toast(page, 'Confirmed 2 records.')).toBeVisible();
    await expect(list.getByRole('listitem')).toHaveCount(3);
    const writes = await sent(page);
    expect(writes).toHaveLength(1);
    expect(writes[0]?.path).toBe('/web/v1/knowledge/confirm');
    expect(writes[0]?.body).toEqual({ ids: [23, 26] });
    expect(writes[0]?.key).toMatch(UUID);
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(toast(page, 'Undone. They are unconfirmed again.')).toBeVisible();
    await expect(list.getByRole('listitem')).toHaveCount(5);
  });

  test('confirm all that match sends the filter, and imported shows what is already confirmed', async ({ page }) => {
    await gotoShell(page, '/knowledge/review');
    await page.getByRole('radio', { name: 'Imported' }).check();
    const list = page.getByRole('list', { name: 'Waiting for review' });
    await expect(list.getByRole('listitem')).toHaveCount(2);
    await expect(list.getByText('Confirmed', { exact: true })).toBeVisible();
    await page.getByLabel('Find a record').fill('swap');
    await expect(list.getByRole('listitem')).toHaveCount(1);
    await page.getByRole('button', { name: 'Confirm all that match' }).click();
    await expect(toast(page, 'Confirmed 1 record.')).toBeVisible();
    await expect(list.getByText('Confirmed', { exact: true })).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => item.path)).toEqual(['/web/v1/knowledge/confirm-matching']);
    expect(writes[0]?.body).toEqual({ state: 'imported', q: 'swap' });
  });

  test('a new fact opens on its own page, unconfirmed, with Undo', async ({ page }) => {
    await gotoShell(page, '/knowledge/subjects');
    await page.getByRole('button', { name: 'New fact' }).click();
    const dialog = page.getByRole('dialog', { name: 'New fact' });
    await dialog.getByRole('button', { name: 'Save the fact' }).click();
    await expect(dialog.getByText('Give the fact a title.')).toBeVisible();
    expect(await sent(page)).toEqual([]);
    await dialog.getByLabel('Title').fill('Compost turns in March');
    await dialog.getByLabel('Text').fill('Turn both bins in the first week.');
    await dialog.getByRole('radio', { name: 'Work' }).check();
    await dialog.getByRole('button', { name: 'Save the fact' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('heading', { level: 1, name: 'Compost turns in March' })).toBeVisible();
    await expect(page.locator('.kpage__tags').getByText('Unconfirmed')).toBeVisible();
    const writes = await sent(page);
    expect(writes).toHaveLength(1);
    expect(writes[0]?.path).toBe('/web/v1/knowledge');
    expect(writes[0]?.body).toEqual({
      title: 'Compost turns in March',
      body: 'Turn both bins in the first week.',
      scope: 'work',
      private: false,
      pinned: false,
    });
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(toast(page, 'Retracted: Compost turns in March. Kept in history.')).toBeVisible();
    await expect(page.locator('.kpage').getByRole('button', { name: 'Restore' })).toBeVisible();
  });

  test('about me shows the budget, why entries are left out, and adds a pinned fact', async ({ page }) => {
    await gotoShell(page, '/knowledge/me');
    await expect(page.getByRole('heading', { level: 1, name: 'About me' })).toBeVisible();
    const facts = page.getByRole('list', { name: 'Facts about you' });
    await expect(facts.getByRole('listitem')).toHaveCount(4);
    await expect(facts.getByRole('listitem').filter({ hasText: 'Lives in Utrecht' })).toContainText('In every run');
    await expect(facts.getByRole('listitem').filter({ hasText: 'Allergic' })).toContainText('Left out: private');
    await expect(facts.getByRole('listitem').filter({ hasText: 'Fridays' })).toContainText('Left out: unconfirmed');
    await expect(facts.getByRole('listitem').filter({ hasText: 'allotment' })).toContainText('Left out: over budget');
    await expect(page.getByText(/^Uses \d+ of 2,000 tokens \(\d+%\)\.$/)).toBeVisible();
    await page.getByText('What the assistant reads').click();
    await expect(page.locator('.kprofile__block pre')).toContainText('- Lives in Utrecht: Moved there in 2019');

    await page.getByRole('button', { name: 'Add a fact about me' }).click();
    const dialog = page.getByRole('dialog', { name: 'Add a fact about me' });
    await dialog.getByLabel('Title').fill('Prefers tea');
    await dialog.getByLabel('Text').fill('Green, no sugar.');
    await dialog.getByRole('button', { name: 'Save the fact' }).click();
    await expect(dialog).toBeHidden();
    await expect(toast(page, 'Saved: Prefers tea.')).toBeVisible();
    await expect(page).toHaveURL(/\/knowledge\/me$/);
    await expect(facts.getByRole('listitem').filter({ hasText: 'Prefers tea' })).toContainText('In every run');
    expect((await sent(page))[0]?.body).toMatchObject({ pinned: true, scope: 'personal' });
  });

  test('retracting a run names the count, starts on Cancel and undoes in one step', async ({ page }) => {
    await gotoShell(page, '/knowledge/23');
    const run = page.getByRole('region', { name: 'From the same run' });
    await expect(run.getByRole('link', { name: /Shed roof needs four sheets/ })).toBeVisible();
    await run.getByRole('button', { name: 'Retract the run' }).click();
    const dialog = page.getByRole('dialog', { name: 'Retract 2 records from this run?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Retract 2 records' }).click();
    await expect(dialog).toBeHidden();
    await expect(toast(page, 'Retracted 2 records from this run. Kept in history.')).toBeVisible();
    await expect(page.locator('.kpage').getByRole('button', { name: 'Restore' })).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => item.path)).toEqual(['/web/v1/knowledge/retract-run']);
    expect(writes[0]?.body).toEqual({ run_id: 61 });
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(toast(page, 'Restored the records from this run.')).toBeVisible();
    await expect(run.getByRole('link', { name: /Shed roof needs four sheets/ })).toBeVisible();
  });

  test('delete for good names the effect, starts on Cancel and leaves the page', async ({ page }) => {
    await retractIlse(page);
    await page.locator('.kpage').getByRole('button', { name: 'Delete for good' }).click();
    const dialog = page.getByRole('dialog', { name: 'Delete Ilse de Wit for good?' });
    await expect(
      dialog.getByText('This removes the page, its links and its history from atrium. It cannot be undone.'),
    ).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    expect((await sent(page)).filter((item) => item.method === 'DELETE')).toEqual([]);
    await page.locator('.kpage').getByRole('button', { name: 'Delete for good' }).click();
    await dialog.getByRole('button', { name: 'Delete for good' }).click();
    await expect(toast(page, 'Deleted for good: Ilse de Wit.')).toBeVisible();
    await expect(page).toHaveURL(/\/knowledge$/);
    await expect(
      page.getByRole('navigation', { name: 'Knowledge subjects' }).getByRole('link', { name: /Ilse de Wit/ }),
    ).toHaveCount(0);
    const deletes = (await sent(page)).filter((item) => item.method === 'DELETE');
    expect(deletes.map((item) => item.path)).toEqual(['/web/v1/knowledge/21']);
    expect(deletes[0]?.key).toMatch(UUID);
  });

  test.describe('when atrium cannot be reached', () => {
    test.use({ allowConsole: unreachableConsole });

    test('writes are refused at once and nothing is sent', async ({ page }) => {
      await gotoShell(page, '/knowledge/review');
      const list = page.getByRole('list', { name: 'Waiting for review' });
      await list.getByRole('checkbox', { name: 'Select Garden weather station' }).check();
      await goUnreachable(page);
      await page.getByRole('button', { name: /^Confirm selected/ }).click();
      await expect(toast(page, 'Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await page.getByRole('button', { name: 'Confirm all that match' }).click();
      await expect(toast(page, 'Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await page.getByRole('button', { name: 'New fact' }).click();
      const dialog = page.getByRole('dialog', { name: 'New fact' });
      await dialog.getByLabel('Title').fill('Compost turns in March');
      await dialog.getByLabel('Text').fill('Turn both bins.');
      await dialog.getByRole('button', { name: 'Save the fact' }).click();
      await expect(dialog.getByText('Cannot reach atrium. Nothing was sent.')).toBeVisible();
      await expect(dialog.getByLabel('Title')).toHaveValue('Compost turns in March');
      expect(await sent(page)).toEqual([]);
    });

    test('a purge whose answer is lost says the outcome is unknown and is not sent again', async ({ page }) => {
      await useScenario(page, { knowledge: { losePurge: true } });
      await retractIlse(page);
      await page.locator('.kpage').getByRole('button', { name: 'Delete for good' }).click();
      const dialog = page.getByRole('dialog', { name: 'Delete Ilse de Wit for good?' });
      await dialog.getByRole('button', { name: 'Delete for good' }).click();
      await expect(
        dialog.getByText(
          'The outcome is unknown. The connection failed before atrium answered, so this may or may not have happened. atrium did not try again.',
        ),
      ).toBeVisible();
      const deletes = async () =>
        (await sent(page)).filter((item) => item.method === 'DELETE').map((item) => item.path);
      expect(await deletes()).toEqual(['/web/v1/knowledge/21']);
      await reconnect(page, /\/web\/v1\/knowledge\/21\/page$/);
      expect(await deletes()).toEqual(['/web/v1/knowledge/21']);
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await gotoShell(page, '/knowledge/review');
      await expect(page.getByRole('list', { name: 'Waiting for review' })).toBeVisible();
      await checkLayout(page, layout.touch);
      await expectFocusVisible(page, 'main');
      await page.getByRole('checkbox', { name: 'Select Garden weather station' }).check();
      await checkLayout(page, layout.touch);

      await gotoShell(page, '/knowledge/me');
      await expect(page.getByRole('list', { name: 'Facts about you' })).toBeVisible();
      await page.getByText('What the assistant reads').click();
      await checkLayout(page, layout.touch);
      await page.getByRole('button', { name: 'Add a fact about me' }).click();
      await expect(page.getByRole('dialog', { name: 'Add a fact about me' })).toBeVisible();
      await checkLayout(page, layout.touch);
      await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

      await gotoShell(page, '/knowledge/23');
      await expect(page.getByRole('region', { name: 'From the same run' })).toBeVisible();
      await checkLayout(page, layout.touch);
      await page.getByRole('button', { name: 'Retract the run' }).click();
      await expect(page.getByRole('dialog', { name: /Retract 2 records/ })).toBeVisible();
      await checkLayout(page, layout.touch);
      await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

      await retractIlse(page);
      await page.locator('.kpage').getByRole('button', { name: 'Delete for good' }).click();
      await expect(page.getByRole('dialog', { name: /for good/ })).toBeVisible();
      await checkLayout(page, layout.touch);
    });
  }

  test.describe('empty and failed states at each width', () => {
    test.use({ allowStatus: [500], allowConsole: [/500/] });

    const states = [
      { scenario: { empty: true }, review: 'Nothing to review', me: 'Nothing about you yet' },
      { scenario: { slow: true }, review: 'Loading the review list', me: 'Loading your profile' },
      {
        scenario: { fail: true },
        review: 'The review list could not be loaded',
        me: 'Your profile could not be loaded',
      },
    ];
    for (const layout of layouts) {
      for (const state of states) {
        test(`design checks for ${Object.keys(state.scenario)[0]} at ${layout.name}`, async ({ page }) => {
          await page.setViewportSize({ width: layout.width, height: layout.height });
          await useScenario(page, { knowledge: state.scenario });
          await gotoShell(page, '/knowledge/review');
          await expect(page.getByText(state.review).first()).toBeAttached({ timeout: 10_000 });
          await checkLayout(page, layout.touch);
          await gotoShell(page, '/knowledge/me');
          await expect(page.getByText(state.me).first()).toBeAttached({ timeout: 10_000 });
          await checkLayout(page, layout.touch);
        });
      }
    }
  });
});
