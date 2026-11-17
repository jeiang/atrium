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

test.describe('search', () => {
  test('typing keeps the query in the URL and groups results by kind', async ({ page }) => {
    await gotoShell(page, '/search');
    const input = page.getByRole('searchbox', { name: 'Search everything' });
    await expect(input).toBeFocused();
    await expect(page.getByText('Search chats, mail, knowledge, tasks and files')).toBeVisible();
    await input.fill('garden');
    await expect(page).toHaveURL(/\/search\?q=garden$/);
    await expect(page.getByRole('heading', { name: 'Best matches' })).toBeVisible();
    const hits = page.locator('[data-hit]');
    await expect(page.locator('[data-hit][data-type="thread"]').first()).toHaveAttribute('href', /\/chat\/401$/);
    await expect(page.locator('[data-hit][data-type="record"]').first()).toHaveAttribute('href', /\/knowledge\/21$/);
    await expect(page.locator('[data-hit][data-type="task"]').first()).toHaveAttribute('href', /\/tasks\/7$/);
    await expect(page.locator('[data-hit][data-type="message"]').first()).toHaveAttribute(
      'href',
      /\/apps\/mail\/message\/501$/,
    );
    await expect(page.locator('[data-hit][data-type="file"]').first()).toHaveAttribute(
      'href',
      /\/web\/v1\/threads\/401\/files\/601\/content$/,
    );
    await expect(hits.locator('mark').first()).toHaveText(/garden/i);
  });

  test('a deep link restores the filter and pages through one kind', async ({ page }) => {
    await gotoShell(page, '/search?q=seed&type=record');
    await expect(page.getByRole('searchbox', { name: 'Search everything' })).toHaveValue('seed');
    await expect(page.getByRole('radio', { name: 'Knowledge' })).toBeChecked();
    const hits = page.locator('[data-hit]');
    await expect(hits).toHaveCount(20);
    await page.getByRole('button', { name: 'Show more' }).click();
    await expect(hits).toHaveCount(25);
    await page.getByRole('radio', { name: 'All' }).click();
    await expect(page).toHaveURL(/\/search\?q=seed$/);
  });

  test('a group can expand into its own filter', async ({ page }) => {
    await gotoShell(page, '/search?q=garden');
    await page.getByRole('button', { name: /^More/ }).first().click();
    await expect(page).toHaveURL(/type=/);
  });

  test('arrow keys move through the results and Escape returns to the field', async ({ page }) => {
    await gotoShell(page, '/search?q=garden');
    const input = page.getByRole('searchbox', { name: 'Search everything' });
    const hits = page.locator('[data-hit]');
    await expect(hits.first()).toBeVisible();
    await input.focus();
    await page.keyboard.press('ArrowDown');
    await expect(hits.nth(0)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(hits.nth(1)).toBeFocused();
    await page.keyboard.press('End');
    await expect(hits.last()).toBeFocused();
    await page.keyboard.press('Home');
    await expect(hits.nth(0)).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(input).toBeFocused();
  });

  test('Cmd or Ctrl K opens search from anywhere', async ({ page }) => {
    await gotoShell(page, '/knowledge');
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
    await page.keyboard.press('ControlOrMeta+k');
    await expect(page).toHaveURL(/\/search$/);
    await expect(page.getByRole('searchbox', { name: 'Search everything' })).toBeFocused();
    await page.keyboard.press('/');
    await expect(page.getByRole('searchbox', { name: 'Search everything' })).toHaveValue('/');
  });

  test('the phone top bar has a search button', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoShell(page, '/');
    await page.locator('.topbar').getByRole('link', { name: 'Search' }).click();
    await expect(page).toHaveURL(/\/search$/);
  });

  test('partial results and no results say so', async ({ page }) => {
    await useScenario(page, { search: { partial: true } });
    await gotoShell(page, '/search?q=garden');
    await expect(page.getByText('Some results may be missing')).toBeVisible();
    await gotoShell(page, '/search?q=zzzz');
    await expect(page.getByText('No results')).toBeVisible();
  });

  test('loading keeps its place', async ({ page }) => {
    await useScenario(page, { search: { slow: true } });
    await gotoShell(page, '/search?q=garden');
    await expect(page.getByText('Searching').first()).toBeAttached();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500], allowConsole: [/500/] });

    test('a failed search offers a retry', async ({ page }) => {
      await useScenario(page, { search: { fail: true } });
      await gotoShell(page, '/search?q=garden');
      await expect(page.getByText('Search did not work')).toBeVisible();
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await gotoShell(page, '/search?q=garden');
      await expect(page.locator('[data-hit]').first()).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
      if (layout.touch) await expectTouchTargets(page);
      await expectNoBlue(page);
      await expectNoSeriousAxeFindings(page);
      await expectFocusVisible(page, 'main');
    });
  }
});
