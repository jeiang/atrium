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
const CURRENT = 'Write in plain words. Ask before sending anything on my behalf.';

async function sent(page: Page): Promise<Sent[]> {
  return page.evaluate(
    () => (globalThis as typeof globalThis & { atriumKnowledgeRequests?: Sent[] }).atriumKnowledgeRequests ?? [],
  );
}

async function openInstructions(page: Page): Promise<void> {
  await gotoShell(page, '/knowledge/instructions');
  await expect(page.getByRole('heading', { level: 1, name: 'Standing instructions' })).toBeVisible();
}

test.describe('standing instructions', () => {
  test('read mode first, then edit with a live preview and save with one keyed write', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openInstructions(page);
    await expect(page.getByText('The assistant reads this page on every run.')).toBeVisible();
    await expect(page.getByRole('textbox')).toHaveCount(0);
    await page.getByRole('button', { name: 'Edit' }).click();
    const editor = page.getByRole('textbox', { name: 'Instructions' });
    await expect(editor).toHaveValue(CURRENT);
    await editor.fill('Write in **plain** words.');
    await expect(page.getByRole('region', { name: 'Preview' }).locator('strong', { hasText: 'plain' })).toBeVisible();
    await expect(page.getByText('Not saved yet')).toBeVisible();
    await editor.press('ControlOrMeta+Enter');
    await expect(page.getByText(/^Saved at /)).toBeVisible();
    await expect(page.locator('.kpage__body strong', { hasText: 'plain' })).toBeVisible();
    const writes = await sent(page);
    expect(writes).toHaveLength(1);
    expect(writes[0]?.method).toBe('PATCH');
    expect(writes[0]?.path).toBe('/web/v1/knowledge/1');
    expect(writes[0]?.key).toMatch(UUID);
    expect(writes[0]?.ifMatch).toContain('2026-11-01T08:15:00.000Z');
    expect(writes[0]?.body).toEqual({ title: 'Standing instructions', body: 'Write in **plain** words.' });
    await expect(page.getByRole('list', { name: 'Versions' }).getByRole('listitem')).toHaveCount(3);
  });

  test('history views, compares and restores only through Save', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openInstructions(page);
    const versions = page.getByRole('list', { name: 'Versions' });
    await expect(versions.getByRole('listitem')).toHaveCount(2);
    await expect(versions.getByRole('listitem').first()).toContainText('Current text, saved by you');
    const earlier = versions.getByRole('listitem').nth(1);
    await expect(earlier).toContainText('Earlier text, saved by the assistant');
    await earlier.getByRole('button', { name: /^View/ }).click();
    await expect(earlier.locator('pre')).toHaveText('Write in plain words.');
    await earlier.getByText('Compare with current').click();
    const diff = earlier.getByRole('list', { name: /Differences from the version/ });
    await expect(diff.locator('.kdiff__line--del')).toContainText('Write in plain words.');
    await expect(diff.locator('.kdiff__line--add')).toContainText(CURRENT);
    await expect(diff.getByText('Only in the current text:')).toBeAttached();
    await earlier.getByRole('button', { name: 'Restore this version' }).click();
    await expect(page.getByText(/^Restoring the version from /)).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Instructions' })).toHaveValue('Write in plain words.');
    expect(await sent(page)).toEqual([]);
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByText(/^Saved at /)).toBeVisible();
    const writes = await sent(page);
    expect(writes.map((item) => item.body)).toEqual([
      { title: 'Standing instructions', body: 'Write in plain words.' },
    ]);
  });

  test.describe('stale', () => {
    test.use({ allowStatus: [409], allowConsole: [/409/] });

    test('a stale save reloads, keeps the draft and shows the newer text', async ({ page }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await openInstructions(page);
      await page.getByRole('button', { name: 'Edit' }).click();
      await page.getByRole('textbox', { name: 'Instructions' }).fill('My draft.');
      await page.evaluate(() =>
        (
          globalThis as typeof globalThis & { atriumKnowledge: { editElsewhere: (id: number) => void } }
        ).atriumKnowledge.editElsewhere(1),
      );
      await page.getByRole('button', { name: 'Save' }).click();
      await expect(page.getByText('The instructions changed since you opened them', { exact: true })).toBeVisible();
      await expect(page.getByRole('textbox', { name: 'Instructions' })).toHaveValue('My draft.');
      await page.getByText('Compare your text with the newer version').click();
      await expect(
        page.getByRole('list', { name: 'Differences between the newer version and your text' }),
      ).toContainText('Sam added a note from the phone.');
      await page.getByRole('button', { name: 'Save' }).click();
      await expect(page.getByText(/^Saved at /)).toBeVisible();
      const writes = await sent(page);
      expect(writes).toHaveLength(2);
      expect(writes[1]?.ifMatch).not.toEqual(writes[0]?.ifMatch);
      expect(writes[1]?.key).not.toEqual(writes[0]?.key);
    });
  });

  // A save that cannot reach the server rejects with a TypeError; fetch is made to fail because the mock
  // service worker answers even when the context is offline.
  test('a save that cannot reach the server keeps the draft', async ({ page }) => {
    await openInstructions(page);
    await page.getByRole('button', { name: 'Edit' }).click();
    await page.getByRole('textbox', { name: 'Instructions' }).fill('Kept draft');
    await page.evaluate(() => {
      const real = window.fetch;
      window.fetch = (input, init) =>
        init?.method && init.method !== 'GET' ? Promise.reject(new TypeError('Failed to fetch')) : real(input, init);
    });
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(
      page.getByText('You are offline. Your text is kept; save again when the connection returns.'),
    ).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Instructions' })).toHaveValue('Kept draft');
    expect(await sent(page)).toEqual([]);
  });

  test('leaving with unsaved text asks first', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await openInstructions(page);
    await page.getByRole('button', { name: 'Edit' }).click();
    await page.getByRole('textbox', { name: 'Instructions' }).fill('Half a thought');
    const subjects = page.getByRole('navigation', { name: 'Knowledge subjects' });
    await subjects.getByRole('link', { name: /Ilse de Wit/ }).click();
    const dialog = page.getByRole('dialog', { name: 'Leave without saving?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Keep editing' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Keep editing' }).click();
    await expect(page).toHaveURL(/\/knowledge\/instructions$/);
    await expect(page.getByRole('textbox', { name: 'Instructions' })).toHaveValue('Half a thought');
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Discard changes' }).click();
    await expect(page.getByRole('button', { name: 'Edit' })).toBeFocused();
    await page.getByRole('button', { name: 'Edit' }).click();
    await page.getByRole('textbox', { name: 'Instructions' }).fill('Another thought');
    await subjects.getByRole('link', { name: /Ilse de Wit/ }).click();
    await dialog.getByRole('button', { name: 'Discard changes' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
    expect(await sent(page)).toEqual([]);
  });

  test('the phone switches between Write and Preview', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await openInstructions(page);
    await page.getByRole('button', { name: 'Edit' }).click();
    await page.getByRole('textbox', { name: 'Instructions' }).fill('# Heading');
    await expect(page.getByRole('region', { name: 'Preview' })).toBeHidden();
    await page.getByRole('radio', { name: 'Preview' }).check();
    await expect(page.getByRole('region', { name: 'Preview' }).getByRole('heading', { name: 'Heading' })).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Instructions' })).toBeHidden();
  });

  test('loading shows a placeholder', async ({ page }) => {
    await useScenario(page, { knowledge: { slow: true } });
    await gotoShell(page, '/knowledge/instructions');
    await expect(page.getByText('Loading knowledge').first()).toBeAttached();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500], allowConsole: [/500/] });

    test('a failed load offers a retry', async ({ page }) => {
      await useScenario(page, { knowledge: { fail: true } });
      await gotoShell(page, '/knowledge/instructions');
      await expect(page.getByText('Knowledge could not be loaded').first()).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Try again' }).first()).toBeVisible();
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await openInstructions(page);
      await page.getByRole('list', { name: 'Versions' }).getByRole('button', { name: /^View/ }).nth(1).click();
      for (const mode of ['read', 'edit']) {
        if (mode === 'edit') await page.getByRole('button', { name: 'Edit' }).click();
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
