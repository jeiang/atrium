import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, layouts, test, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];
const weatherOn = { apps: { on: ['weather'] } };
const opsOn = { apps: { on: ['grafana'] } };
const location = '/settings/connections/service/weather';

test.describe('the weather page', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('shows the conditions now, the next hours and the week', async ({ page }) => {
    await useScenario(page, weatherOn);
    await gotoShell(page, '/apps');
    await page.locator('[data-app="weather"]').getByRole('link', { name: 'Weather', exact: true }).click();
    await expect(page).toHaveURL(/\/apps\/weather$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Weather' })).toBeVisible();
    await expect(page.locator('.page-lede')).toHaveText('Hillfield');
    const now = page.locator('.card', { has: page.getByRole('heading', { level: 2, name: 'Now' }) });
    await expect(now.locator('.wx-now')).toContainText('6°');
    await expect(now.locator('.wx-now')).toContainText('Overcast');
    await expect(now.getByText('Feels like 3°')).toBeVisible();
    await expect(now.getByText('18 km/h SW')).toBeVisible();
    await expect(now.getByText('82%')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Next hours' }).getByRole('listitem')).toHaveCount(12);
    const days = page.getByRole('list', { name: 'Days' }).getByRole('listitem');
    await expect(page.getByRole('heading', { level: 2, name: 'Next 7 days' })).toBeVisible();
    await expect(days).toHaveCount(7);
    await expect(days.first()).toContainText('Today');
    await expect(days.first()).toContainText('Slight rain');
    await expect(days.first()).toContainText('3° / 8°');
    await expect(page.getByRole('link', { name: 'Change location' })).toHaveAttribute('href', location);
  });

  test('without a location it links to the place setting', async ({ page }) => {
    await useScenario(page, { ...weatherOn, weather: { state: 'unconfigured' } });
    await gotoShell(page, '/apps/weather');
    await expect(page.getByRole('heading', { name: 'No location set' })).toBeVisible();
    await page.getByRole('link', { name: 'Set a location' }).click();
    await expect(page).toHaveURL(new RegExp(`${location}$`));
  });

  test('a forecast that is off says so', async ({ page }) => {
    await useScenario(page, { ...weatherOn, weather: { state: 'off' } });
    await gotoShell(page, '/apps/weather');
    await expect(page.getByRole('heading', { name: 'The forecast is off' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Weather connection' })).toHaveAttribute('href', location);
  });

  test('a failed forecast says why', async ({ page }) => {
    await useScenario(page, { ...weatherOn, weather: { state: 'failed' } });
    await gotoShell(page, '/apps/weather');
    await expect(page.getByRole('heading', { name: 'The forecast did not load' })).toBeVisible();
    await expect(page.getByText('The weather service took too long to answer.')).toBeVisible();
  });

  test('a stale forecast shows its age', async ({ page }) => {
    await useScenario(page, { ...weatherOn, weather: { state: 'stale' } });
    await gotoShell(page, '/apps/weather');
    await expect(page.getByText('Last updated 3 hours ago.')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Days' }).getByRole('listitem')).toHaveCount(7);
  });

  test('is not found while the app is off', async ({ page }) => {
    await gotoShell(page, '/apps/weather');
    await expect(page.getByRole('heading', { level: 1, name: 'Not found' })).toBeVisible();
  });
});

test.describe('the server ops page', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('shows host health, firing alerts and findings', async ({ page }) => {
    await useScenario(page, opsOn);
    await gotoShell(page, '/apps');
    await page.locator('[data-app="grafana"]').getByRole('link', { name: 'Server ops', exact: true }).click();
    await expect(page).toHaveURL(/\/apps\/grafana$/);
    await expect(page.getByRole('heading', { level: 1, name: 'Server ops' })).toBeVisible();
    await expect(page.locator('.page-lede')).toHaveText('2 alerts firing, 1 host down');
    const larch = page.locator('[data-host="larch"]');
    await expect(larch.getByText('Down', { exact: true })).toBeVisible();
    await expect(larch.getByText('71%')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Alerts firing on larch' }).getByRole('listitem')).toHaveText([
      'HostDown',
      'HighCpu',
    ]);
    const rowan = page.locator('[data-host="rowan"]');
    await expect(rowan.getByText('Up', { exact: true })).toBeVisible();
    await expect(rowan.getByRole('list', { name: /^Alerts firing/ })).toHaveCount(0);
    const findings = page.getByRole('list', { name: 'Findings' }).getByRole('listitem');
    await expect(findings).toHaveCount(2);
    await expect(findings.first()).toContainText('Critical');
    await expect(findings.first()).toContainText('up changed from up to down at the last sample.');
    await expect(findings.first().getByRole('link', { name: 'Host larch is down' })).toHaveAttribute(
      'href',
      '/knowledge/68',
    );
    await expect(findings.nth(1)).toContainText('Warning');
    const grafana = page.getByRole('link', { name: 'Open Grafana' });
    await expect(grafana).toHaveAttribute('href', 'https://grafana.example.com/');
    await expect(grafana).toHaveAttribute('target', '_blank');
  });

  test('before any sample it points to the connection', async ({ page }) => {
    await useScenario(page, { ...opsOn, serverOps: { empty: true, noGrafana: true } });
    await gotoShell(page, '/apps/grafana');
    await expect(page.locator('.page-lede')).toHaveText('No samples yet');
    await expect(page.getByRole('heading', { name: 'No samples yet' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'No open findings' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open Grafana' })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Grafana connection' }).first()).toHaveAttribute(
      'href',
      '/settings/connections/service/grafana',
    );
  });
});

test.describe('app pages design checks', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });

      const screens = [
        { name: 'the weather page', path: '/apps/weather', scenario: weatherOn, ready: '.wx-days' },
        { name: 'the server ops page', path: '/apps/grafana', scenario: opsOn, ready: '.ops-findings' },
        {
          name: 'the empty server ops page',
          path: '/apps/grafana',
          scenario: { ...opsOn, serverOps: { empty: true } },
          ready: '.statebox',
        },
      ];
      for (const screen of screens) {
        test(`${screen.name} fits, rounds and passes axe`, async ({ page }) => {
          await useScenario(page, screen.scenario);
          await gotoShell(page, screen.path);
          await expect(page.locator(screen.ready).first()).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});
