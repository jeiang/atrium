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
import { expect, layouts, test, useScenario } from './fixtures';

interface Sent {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function sent(page: Page): Promise<Sent[]> {
  return page.evaluate(
    () => (globalThis as typeof globalThis & { atriumKnowledgeRequests?: Sent[] }).atriumKnowledgeRequests ?? [],
  );
}

async function openGarden(page: Page): Promise<void> {
  await gotoShell(page, '/knowledge/spaces/5');
  await expect(page.getByRole('heading', { level: 1, name: 'Shared garden' })).toBeVisible();
}

test.describe('knowledge spaces', () => {
  test('the list leads to a space with its subjects, chats, tasks and files', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/subjects');
    await page.getByRole('link', { name: /^Spaces/ }).click();
    await expect(page).toHaveURL(/\/knowledge\/spaces$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Spaces' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Spaces' });
    await expect(list.getByRole('listitem')).toHaveCount(2);
    await expect(list.getByRole('listitem').filter({ hasText: 'Studio move' })).toContainText('Private');
    await list.getByRole('link', { name: /Shared garden/ }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Shared garden' })).toBeVisible();
    await expect(
      page.getByRole('navigation', { name: 'Spaces' }).getByRole('link', { name: /Shared garden/ }),
    ).toHaveAttribute('aria-current', 'true');
    await expect(page.locator('.prose strong', { hasText: 'short' })).toBeVisible();
    await expect(
      page.getByRole('list', { name: 'Subjects in this space' }).getByRole('link', { name: /Shared garden project/ }),
    ).toHaveAttribute('href', '/knowledge/22');
    await expect(
      page.getByRole('list', { name: 'Chats in this space' }).getByRole('link', { name: /Planning the raised beds/ }),
    ).toHaveAttribute('href', '/chat/401');
    await expect(
      page.getByRole('list', { name: 'Tasks in this space' }).getByRole('link', { name: /Order raised bed timber/ }),
    ).toHaveAttribute('href', '/tasks/7');
    await expect(page.getByRole('list', { name: 'Files in this space' })).toContainText('garden-seeds.csv');
  });

  test('a subject page links to its space', async ({ page }) => {
    await gotoShell(page, '/knowledge/22');
    await page
      .getByRole('region', { name: 'Links' })
      .getByRole('link', { name: /Shared garden/ })
      .first()
      .click();
    await expect(page).toHaveURL(/\/knowledge\/spaces\/5$/);
  });

  test('adding a subject sends one keyed link and Undo takes it out again', async ({ page }) => {
    await openGarden(page);
    await page.getByRole('button', { name: 'Add a subject' }).click();
    const dialog = page.getByRole('dialog', { name: 'Add a subject to Shared garden' });
    await dialog.getByRole('combobox', { name: 'Subject' }).fill('weather');
    await dialog.getByRole('option', { name: /Garden weather station/ }).click();
    await dialog.getByRole('button', { name: 'Add to the space' }).click();
    await expect(
      page.locator('.toast__text', { hasText: 'Added Garden weather station to Shared garden.' }),
    ).toBeVisible();
    const subjects = page.getByRole('list', { name: 'Subjects in this space' });
    await expect(subjects.getByRole('link', { name: /Garden weather station/ })).toBeVisible();
    await page.locator('.toast').getByRole('button', { name: 'Undo' }).click();
    await expect(subjects.getByRole('link', { name: /Garden weather station/ })).toHaveCount(0);
    const writes = await sent(page);
    expect(writes.map((item) => item.path)).toEqual(['/web/v1/knowledge/23/links', '/web/v1/effects/900/undo']);
    expect(writes[0]?.body).toEqual({ target_kind: 'space', target_id: '5', relation: 'part_of' });
    expect(writes[0]?.key).toMatch(UUID);
  });

  test('removing a subject offers Undo', async ({ page }) => {
    await openGarden(page);
    await page.getByRole('button', { name: 'Take Shared garden project out of Shared garden' }).click();
    await expect(
      page.locator('.toast__text', { hasText: 'Took Shared garden project out of Shared garden.' }),
    ).toBeVisible();
    const subjects = page.getByText('No subjects in this space yet.');
    await expect(subjects).toBeVisible();
    await page.locator('.toast').getByRole('button', { name: 'Undo' }).click();
    await expect(
      page.getByRole('list', { name: 'Subjects in this space' }).getByRole('link', { name: /Shared garden project/ }),
    ).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => item.path)).toEqual(['/web/v1/knowledge/links/76/retract', '/web/v1/effects/900/undo']);
  });

  test('the keyboard reaches every space action', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openGarden(page);
    await page.getByRole('button', { name: 'Rename' }).focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Rename Shared garden' });
    await expect(dialog.getByLabel('Name')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Rename' })).toBeFocused();
  });

  test('a new space opens once made, and renaming it updates the list', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/spaces');
    await page.getByRole('button', { name: 'New space' }).click();
    const dialog = page.getByRole('dialog', { name: 'New space' });
    await dialog.getByRole('button', { name: 'Make the space' }).click();
    await expect(dialog.getByText('Give the space a name.')).toBeVisible();
    await dialog.getByLabel('Name').fill('Allotment');
    await dialog.getByLabel('Private').check();
    await dialog.getByRole('button', { name: 'Make the space' }).click();
    await expect(page).toHaveURL(/\/knowledge\/spaces\/10$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Allotment' })).toBeVisible();
    await page.getByRole('button', { name: 'Rename' }).click();
    const rename = page.getByRole('dialog', { name: 'Rename Allotment' });
    await rename.getByLabel('Name').fill('Allotment 12');
    await rename.getByRole('button', { name: 'Rename' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Allotment 12' })).toBeVisible();
    await expect(
      page.getByRole('navigation', { name: 'Spaces' }).getByRole('link', { name: /Allotment 12/ }),
    ).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => `${item.method} ${item.path}`)).toEqual([
      'POST /web/v1/knowledge/spaces',
      'PATCH /web/v1/knowledge/spaces/10',
    ]);
    expect(writes[0]?.body).toEqual({ name: 'Allotment', effort: 'xhigh', private: true });
    expect(writes[1]?.body).toEqual({ name: 'Allotment 12' });
    for (const write of writes) expect(write.key).toMatch(UUID);
  });

  test('the phone shows the list, then the space with a way back', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoShell(page, '/knowledge/spaces');
    await page.getByRole('link', { name: /Shared garden/ }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Shared garden' })).toBeVisible();
    await expect(page.getByRole('navigation', { name: 'Spaces' })).toHaveCount(0);
    await page.getByRole('link', { name: 'All spaces' }).click();
    await expect(page).toHaveURL(/\/knowledge\/spaces$/);
  });

  test('an empty store says so', async ({ page }) => {
    await useScenario(page, { knowledge: { empty: true } });
    await gotoShell(page, '/knowledge/spaces');
    await expect(page.getByText('No spaces yet')).toBeVisible();
  });

  test('a space with nothing in it says so per section', async ({ page }) => {
    await gotoShell(page, '/knowledge/spaces/6');
    await expect(page.getByRole('heading', { level: 1, name: 'Studio move' })).toBeVisible();
    await expect(page.getByText('No instructions for this space.')).toBeVisible();
    await expect(page.getByText('No subjects in this space yet.')).toBeVisible();
    await expect(page.getByText('No files in this space yet.')).toBeVisible();
  });

  test('loading shows a placeholder', async ({ page }) => {
    await useScenario(page, { knowledge: { slow: true } });
    await gotoShell(page, '/knowledge/spaces/5');
    await expect(page.getByText('Loading the space').first()).toBeAttached();
    await expect(page.getByText('Loading spaces').first()).toBeAttached();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500, 404], allowConsole: [/500|404/] });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { knowledge: { fail: true } });
      await gotoShell(page, '/knowledge/spaces/5');
      await expect(page.getByText('This space could not be loaded')).toBeVisible({ timeout: 10_000 });
      await expect(page.getByText('Spaces could not be loaded')).toBeVisible();
    });

    test('a missing space says so', async ({ page }) => {
      await gotoShell(page, '/knowledge/spaces/99');
      await expect(page.getByText('This space does not exist')).toBeVisible();
    });
  });

  test('offline says so', async ({ page, context }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openGarden(page);
    await context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await page
      .getByRole('navigation', { name: 'Spaces' })
      .getByRole('link', { name: /Studio move/ })
      .click();
    await expect(page.getByText('You are offline').first()).toBeVisible();
    await context.setOffline(false);
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      for (const path of ['/knowledge/spaces', '/knowledge/spaces/5']) {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
        await expect(page.getByText(/Loading/)).toHaveCount(0);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await expectNoSeriousAxeFindings(page);
        await expectFocusVisible(page, 'main');
      }
      await page.getByRole('button', { name: 'Add a subject' }).click();
      await expect(page.getByRole('dialog', { name: 'Add a subject to Shared garden' })).toBeVisible();
      if (layout.touch) await expectTouchTargets(page, 'dialog');
      await expectNoSeriousAxeFindings(page);
    });
  }

  // The base has no shared offline state for writes: a write that cannot reach the server rejects with a
  // TypeError. The mock service worker answers even when the context is offline, so fetch is made to fail.
  test('a write that cannot reach the server keeps the dialog and its text', async ({ page }) => {
    await openGarden(page);
    await page.evaluate(() => {
      const real = window.fetch;
      window.fetch = (input, init) =>
        init?.method && init.method !== 'GET' ? Promise.reject(new TypeError('Failed to fetch')) : real(input, init);
    });
    await page.getByRole('button', { name: 'Rename' }).click();
    const dialog = page.getByRole('dialog', { name: 'Rename Shared garden' });
    await dialog.getByLabel('Name').fill('Garden');
    await dialog.getByRole('button', { name: 'Rename' }).click();
    await expect(dialog.getByText('You are offline. Nothing was renamed.')).toBeVisible();
    await expect(dialog.getByLabel('Name')).toHaveValue('Garden');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await page.getByRole('button', { name: 'Take Shared garden project out of Shared garden' }).click();
    await expect(page.locator('.toast__text', { hasText: 'You are offline. Nothing changed.' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Subjects in this space' }).getByRole('listitem')).toHaveCount(1);
    expect(await sent(page)).toEqual([]);
  });
});
