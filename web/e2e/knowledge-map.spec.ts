import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures';

test.describe('knowledge map', () => {
  test('M opens the map of the open subject and Escape returns with focus on Map', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/21');
    await expect(page.getByRole('heading', { level: 1, name: 'Ilse de Wit' })).toBeVisible();
    await page.keyboard.press('m');
    await expect(page).toHaveURL(/\/knowledge\/map\?subject=21$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Map of Ilse de Wit' })).toBeVisible();
    const map = page.locator('svg.kmap__svg');
    await expect(map).toBeVisible();
    await expect(map.getByRole('link', { name: 'Garden weather station', exact: true })).toBeVisible();
    await expect(map.getByRole('link', { name: 'Shared garden project', exact: true })).toBeVisible();
    await expect(map.getByRole('link', { name: 'Water the tomatoes in the morning, private' })).toBeVisible();
    await expect(map.getByRole('link', { name: /Old compost plan/ })).toHaveCount(0);
    const also = page.getByRole('region', { name: 'Also linked' });
    await expect(also.getByRole('link', { name: /Old compost plan/ })).toContainText('retracted, so not on the map');
    await expect(also.getByRole('link', { name: /Order raised bed timber/ })).toContainText('not a subject');
    await page.keyboard.press('Escape');
    await expect(page).toHaveURL(/\/knowledge\/21$/);
    await expect(page.getByRole('button', { name: /^Map/ })).toBeFocused();
  });

  test('L and M switch between the list twin and the map', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/map?subject=21');
    await expect(page.locator('svg.kmap__svg')).toBeVisible();
    await page.keyboard.press('l');
    await expect(page.locator('svg.kmap__svg')).toHaveCount(0);
    const here = page.getByRole('list', { name: 'Links here' });
    await expect(here.getByRole('link', { name: /Garden weather station/ }).first()).toBeVisible();
    await expect(page.getByRole('list', { name: 'Linked from this subject' }).getByRole('listitem')).toHaveCount(2);
    await expect(page.getByText('Private', { exact: true })).toBeVisible();
    await page.keyboard.press('m');
    await expect(page.locator('svg.kmap__svg')).toBeVisible();
  });

  test('a deep link opened directly goes back to the page on Escape', async ({ page }) => {
    await gotoShell(page, '/knowledge/map?subject=23');
    await expect(page.getByRole('heading', { level: 1, name: 'Map of Garden weather station' })).toBeVisible();
    await page
      .locator('svg.kmap__svg')
      .getByRole('link', { name: /^Ilse de Wit/ })
      .click();
    await expect(page).toHaveURL(/\/knowledge\/map\?subject=21$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Map of Ilse de Wit' })).toBeVisible();
    await page.getByRole('button', { name: 'Back to the page' }).click();
    await expect(page).toHaveURL(/\/knowledge\/21$/);
  });

  test('many links show twelve first and more on request', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/map?subject=22');
    await expect(page.getByText('Showing 12 of 21 linked subjects.')).toBeVisible();
    await expect(page.locator('svg.kmap__svg a')).toHaveCount(13);
    await page.getByRole('button', { name: 'Show 9 more' }).click();
    await expect(page.locator('svg.kmap__svg a')).toHaveCount(22);
    await expect(page.getByText('21 linked subjects')).toBeVisible();
  });

  test('a subject without links says so', async ({ page }) => {
    await gotoShell(page, '/knowledge/map?subject=150');
    await expect(page.getByText('Nothing links to this subject yet')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open the page' })).toHaveAttribute('href', '/knowledge/150');
  });

  test('a map without a subject asks for one', async ({ page }) => {
    await gotoShell(page, '/knowledge/map');
    await expect(page.getByText('Pick a subject to map')).toBeVisible();
  });

  test('M in a text field types the letter', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoShell(page, '/knowledge/21');
    await page.getByLabel('Find a subject').fill('');
    await page.getByLabel('Find a subject').press('m');
    await expect(page.getByLabel('Find a subject')).toHaveValue('m');
    await expect(page).toHaveURL(/\/knowledge\/21$/);
  });

  test('the phone shows only the list twin', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoShell(page, '/knowledge/21');
    await page.getByRole('button', { name: /^Map/ }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Map of Ilse de Wit' })).toBeVisible();
    await expect(page.locator('svg.kmap__svg')).toHaveCount(0);
    await expect(page.getByRole('radiogroup')).toHaveCount(0);
    await expect(page.getByRole('list', { name: 'Links here' })).toBeVisible();
  });

  test('loading keeps the space and failure offers a retry', async ({ page }) => {
    await useScenario(page, { knowledge: { slow: true } });
    await gotoShell(page, '/knowledge/map?subject=21');
    await expect(page.getByText('Loading the map').first()).toBeAttached();
  });

  test.describe('failures', () => {
    test.use({ allowStatus: [500, 404], allowConsole: [/500|404/] });

    test('a failed map offers a retry', async ({ page }) => {
      await useScenario(page, { knowledge: { fail: true } });
      await gotoShell(page, '/knowledge/map?subject=21');
      await expect(page.getByText('The map could not be loaded')).toBeVisible({ timeout: 10_000 });
      await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();
    });

    test('a missing subject says so', async ({ page }) => {
      await gotoShell(page, '/knowledge/map?subject=999');
      await expect(page.getByText('This subject does not exist')).toBeVisible();
    });
  });

  test.describe('when atrium cannot be reached', () => {
    test.use({ allowConsole: unreachableConsole });

    test('an unopened subject says so', async ({ page }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await gotoShell(page, '/knowledge/map?subject=23');
      await expect(page.getByRole('heading', { level: 1, name: 'Map of Garden weather station' })).toBeVisible();
      await goUnreachable(page);
      await page
        .locator('svg.kmap__svg')
        .getByRole('link', { name: /^Ilse de Wit/ })
        .click();
      await expect(page.getByRole('heading', { name: 'Cannot reach atrium' })).toBeVisible();
    });
  });

  for (const layout of layouts) {
    test(`design checks at ${layout.name}`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await gotoShell(page, '/knowledge/map?subject=21');
      await expect(page.getByRole('heading', { level: 1, name: 'Map of Ilse de Wit' })).toBeVisible();
      await expect(page.getByRole('region', { name: 'Also linked' })).toBeVisible();
      await expectNoHorizontalScroll(page);
      await expectRoundedSurfaces(page, ['.rail', '.tabbar', 'dialog.sheet']);
      if (layout.touch) await expectTouchTargets(page);
      await expectNoBlue(page);
      await expectNoSeriousAxeFindings(page);
      await expectFocusVisible(page, 'main');
    });
  }
});
