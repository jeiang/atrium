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
  ifMatch: string | null;
  body: unknown;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function sent(page: Page): Promise<Sent[]> {
  return page.evaluate(
    () => (globalThis as typeof globalThis & { atriumKnowledgeRequests?: Sent[] }).atriumKnowledgeRequests ?? [],
  );
}

async function openIlse(page: Page): Promise<void> {
  await gotoShell(page, '/knowledge');
  await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
}

test.describe('knowledge', () => {
  test('opens on the newest subject with facts, links both ways and a metadata margin', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openIlse(page);
    const subjects = page.getByRole('navigation', { name: 'Knowledge subjects' });
    await expect(subjects.getByRole('link', { name: /Standing instructions/ })).toBeVisible();
    await expect(subjects.getByRole('link', { name: /Ilse de Wit/ })).toHaveAttribute('aria-current', 'true');
    const facts = page.getByRole('list', { name: 'Facts' });
    await expect(facts.getByRole('listitem')).toHaveCount(3);
    await expect(facts.getByText('Unconfirmed')).toBeVisible();
    const from = page.getByRole('region', { name: 'Links' });
    await expect(from.getByRole('link', { name: /Shared garden project/ })).toBeVisible();
    await expect(from.getByRole('link', { name: /Order raised bed timber/ })).toBeVisible();
    await expect(from.getByRole('link', { name: /Garden weather station/ })).toBeVisible();
    await expect(page.locator('aside.kpage__margin')).toBeVisible();
    await facts.getByRole('link', { name: /Birthday/ }).click();
    await expect(page).toHaveURL(/\/knowledge\/31$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Birthday' })).toBeVisible();
  });

  test('the phone keeps metadata behind Details and the list one tap away', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await openIlse(page);
    await expect(page.locator('aside.kpage__margin')).toBeHidden();
    const details = page.locator('details.kpage__details');
    await details.locator('summary').click();
    await expect(details.getByText('Written by')).toBeVisible();
    await page.getByRole('link', { name: 'All subjects' }).click();
    await expect(page).toHaveURL(/\/knowledge\/subjects$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Knowledge' })).toBeVisible();
  });

  test('the list filters and pages by cursor', async ({ page }) => {
    await gotoShell(page, '/knowledge/subjects');
    const list = page.getByRole('list', { name: 'Subjects' });
    await expect(list.getByRole('listitem')).toHaveCount(49);
    await page.getByRole('button', { name: 'Show more' }).click();
    await expect(list.getByRole('listitem')).toHaveCount(60);
    await page.getByLabel('Find a subject').fill('weather');
    await expect(list.getByRole('listitem')).toHaveCount(1);
    await expect(list.getByRole('link', { name: /Garden weather station/ })).toBeVisible();
  });

  test('removing a link undoes once with a fresh key per gesture', async ({ page }) => {
    await openIlse(page);
    await page.getByRole('button', { name: 'Remove the link to Shared garden project' }).click();
    await expect(page.locator('.toast__text', { hasText: 'Removed the link to Shared garden project.' })).toBeVisible();
    const links = page.getByRole('region', { name: 'Links' });
    await expect(links.getByRole('link', { name: /Shared garden project/ })).toHaveCount(0);
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(links.getByRole('link', { name: /Shared garden project/ })).toBeVisible();
    const writes = (await sent(page)).filter((item) => item.method !== 'GET');
    expect(writes.map((item) => item.path)).toEqual(['/web/v1/knowledge/links/71/retract', '/web/v1/effects/900/undo']);
    expect(writes[0]?.key).toMatch(UUID);
    expect(writes[1]?.key).toMatch(UUID);
    expect(writes[0]?.key).not.toBe(writes[1]?.key);
  });

  test('adding a link sends one request and shows it on the page', async ({ page }) => {
    await openIlse(page);
    await page.getByRole('button', { name: 'Add a link' }).click();
    const dialog = page.getByRole('dialog', { name: 'Link Ilse de Wit' });
    const target = dialog.getByRole('combobox', { name: 'Link to' });
    await target.fill('seed catalogue note 1');
    await expect(dialog.getByRole('option', { name: /^Seed catalogue note 1 / }).first()).toBeAttached();
    await target.press('ArrowDown');
    await target.press('Enter');
    await dialog.getByLabel('How they relate').selectOption('depends_on');
    await dialog.getByRole('button', { name: 'Add link' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.locator('.toast__text', { hasText: /Linked to Seed catalogue note 1 as/ })).toBeVisible();
    await expect(
      page.getByRole('region', { name: 'Links' }).getByRole('link', { name: /Seed catalogue note 1/ }),
    ).toBeVisible();
    const writes = (await sent(page)).filter((item) => item.method !== 'GET');
    expect(writes).toHaveLength(1);
    expect(writes[0]?.path).toBe('/web/v1/knowledge/21/links');
    expect(writes[0]?.body).toEqual({ target_kind: 'record', target_id: '100', relation: 'depends_on' });
    expect(writes[0]?.key).toMatch(UUID);
  });

  test.describe('a stale save', () => {
    test.use({ allowStatus: [409], allowConsole: [/409/] });

    test('a stale save reloads, says so and keeps the draft', async ({ page }) => {
      await openIlse(page);
      await page.getByRole('button', { name: 'Edit' }).click();
      const text = page.getByLabel('Text');
      await text.fill('Ilse runs the garden.');
      await page.evaluate(() =>
        (
          globalThis as typeof globalThis & { atriumKnowledge: { editElsewhere: (id: number) => void } }
        ).atriumKnowledge.editElsewhere(21),
      );
      await page.getByRole('button', { name: 'Save' }).click();
      await expect(page.getByText('This page changed since you opened it', { exact: true })).toBeVisible();
      await expect(text).toHaveValue('Ilse runs the garden.');
      await page.getByRole('button', { name: 'Save' }).click();
      await expect(page.getByText(/Saved at/)).toBeVisible();
      await expect(page.getByText('Ilse runs the garden.')).toBeVisible();
      const saves = (await sent(page)).filter((item) => item.method === 'PATCH');
      expect(saves).toHaveLength(2);
      expect(saves[0]?.ifMatch).toBeTruthy();
      expect(saves[1]?.ifMatch).not.toBe(saves[0]?.ifMatch);
      expect(saves[0]?.key).not.toBe(saves[1]?.key);
    });
  });

  test('retract offers Undo and the banner restores', async ({ page }) => {
    await openIlse(page);
    await page.getByRole('button', { name: 'Retract' }).click();
    await expect(page.locator('.toast__text', { hasText: 'Retracted: Ilse de Wit. Kept in history.' })).toBeVisible();
    const banner = page.locator('.kpage').getByRole('button', { name: 'Restore' });
    await expect(banner).toBeVisible();
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(banner).toBeHidden();
    await page.getByRole('button', { name: 'Retract' }).click();
    await expect(banner).toBeVisible();
    await banner.click();
    await expect(page.locator('.toast__text', { hasText: 'Restored: Ilse de Wit.' }).last()).toBeVisible();
    await expect(banner).toBeHidden();
  });

  test('instructions, map and spaces each open on their own route', async ({ page }) => {
    await gotoShell(page, '/knowledge/instructions');
    await expect(page.getByRole('heading', { level: 1, name: 'Standing instructions' })).toBeVisible();
    await expect(page.getByText('The assistant reads this page on every run.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Edit' })).toBeVisible();
    await gotoShell(page, '/knowledge/1');
    await expect(page.getByRole('list', { name: 'Versions' })).toBeVisible();
    await gotoShell(page, '/knowledge/map?subject=21');
    await expect(page.getByRole('heading', { level: 1, name: 'Map of Ilse de Wit' })).toBeVisible();
    await gotoShell(page, '/knowledge/spaces');
    await expect(page.getByRole('heading', { level: 1, name: 'Spaces' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Spaces' }).getByRole('listitem')).toHaveCount(2);
  });

  test('an empty store says so', async ({ page }) => {
    await useScenario(page, { knowledge: { empty: true } });
    await gotoShell(page, '/knowledge/subjects');
    await expect(page.getByText('Nothing saved yet')).toBeVisible();
  });

  test('loading shows a placeholder of the same size', async ({ page }) => {
    await useScenario(page, { knowledge: { slow: true } });
    await gotoShell(page, '/knowledge');
    await expect(page.getByText('Loading knowledge').first()).toBeAttached();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500], allowConsole: [/500/] });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { knowledge: { fail: true } });
      await gotoShell(page, '/knowledge');
      await expect(page.getByText('Knowledge could not be loaded').first()).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Try again' }).first()).toBeVisible();
    });

    test('failed links keep the page readable', async ({ page }) => {
      await useScenario(page, { knowledge: { failLinks: true } });
      await openIlse(page);
      await expect(page.getByText('Links could not be loaded')).toBeVisible();
      await expect(page.getByRole('list', { name: 'Facts' }).getByRole('listitem')).toHaveCount(3);
    });
  });

  test('offline says so', async ({ page, context }) => {
    await openIlse(page);
    await context.setOffline(true);
    await page.evaluate(() => window.dispatchEvent(new Event('offline')));
    await page
      .getByRole('navigation', { name: 'Knowledge subjects' })
      .getByRole('link', { name: /Shared garden project/ })
      .click();
    await expect(page.getByText('You are offline').first()).toBeVisible();
    await context.setOffline(false);
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      for (const path of ['/knowledge', '/knowledge/subjects']) {
        await gotoShell(page, path);
        await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await expectNoSeriousAxeFindings(page);
        await expectFocusVisible(page, 'main');
      }
    });
  }
});
