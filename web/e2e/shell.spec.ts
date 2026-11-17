import { readFileSync } from 'node:fs';
import {
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, layouts, routes, test } from './fixtures.ts';

const edgeSurfaces = ['.rail', '.tabbar'];

function token(scheme: 'light' | 'dark', name: string): string {
  const file = new URL(`../../design/tokens/color.${scheme}.tokens.json`, import.meta.url);
  const [l, c, h] = JSON.parse(readFileSync(file, 'utf8')).color[name].$value.components;
  return `oklch(${l} ${c} ${h})`;
}

test('the shell renders in the mock build', async ({ page }) => {
  await gotoShell(page, '/');
  await expect(page).toHaveTitle('Home · atrium');
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Home' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Home', exact: true })).toHaveAttribute('aria-current', 'page');
});

test('a route change moves focus to the heading and announces it', async ({ page }) => {
  await gotoShell(page, '/');
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Apps', exact: true }).click();
  await expect(page).toHaveURL(/\/apps$/);
  await expect(page.getByRole('heading', { level: 1, name: 'Apps' })).toBeFocused();
  await expect(page.locator('[role="status"][aria-live="polite"]')).toHaveText('Apps');
  await expect(page).toHaveTitle('Apps · atrium');
});

test('the production policy enforces Trusted Types', async ({ page, problems }) => {
  const response = await page.goto('/');
  expect(response?.headers()['content-security-policy']).toContain("require-trusted-types-for 'script'");
  await expect(page.getByRole('button', { name: 'Account: Operator' })).toBeVisible();
  const outcome = await page.evaluate(() => {
    try {
      document.createElement('div').innerHTML = '<b>x</b>';
      return 'assigned';
    } catch (error) {
      return error instanceof TypeError ? 'refused' : String(error);
    }
  });
  expect(outcome).toBe('refused');
  await expect.poll(() => problems.some((line) => line.includes('trusted-types'))).toBe(true);
  problems.length = 0;
});

for (const scheme of ['light', 'dark'] as const) {
  test(`the ${scheme} scheme applies the tokens`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await gotoShell(page, '/');
    const colors = await page.evaluate(
      ({ paper, ink }) => {
        const canvas = document.createElement('canvas');
        canvas.width = 1;
        canvas.height = 1;
        const context = canvas.getContext('2d', { willReadFrequently: true });
        if (!context) throw new Error('no 2d canvas');
        const rgba = (css: string): number[] => {
          context.clearRect(0, 0, 1, 1);
          context.fillStyle = css;
          context.fillRect(0, 0, 1, 1);
          return [...context.getImageData(0, 0, 1, 1).data];
        };
        const body = getComputedStyle(document.body);
        return {
          background: rgba(body.backgroundColor),
          text: rgba(body.color),
          paper: rgba(paper),
          ink: rgba(ink),
        };
      },
      { paper: token(scheme, 'paper'), ink: token(scheme, 'ink') },
    );
    expect(colors.background, 'body background is --paper').toEqual(colors.paper);
    expect(colors.text, 'body text is --ink').toEqual(colors.ink);
    expect(colors.background).not.toEqual(colors.text);
  });
}

test('light and dark schemes differ', async ({ page }) => {
  await gotoShell(page, '/');
  const read = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const light = await read();
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect.poll(read).not.toBe(light);
});

test.describe('accessibility', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });
      for (const route of [...routes, { path: '/missing', title: 'Not found' }]) {
        test(`axe on ${route.path}`, async ({ page }) => {
          await gotoShell(page, route.path);
          await expect(page.getByRole('heading', { level: 1, name: route.title })).toBeVisible();
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});

test.describe('layout', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });
      for (const route of routes) {
        test(`${route.path} fits, rounds its surfaces${layout.touch ? ' and meets the touch rule' : ''}`, async ({
          page,
        }) => {
          await gotoShell(page, route.path);
          await expect(page.getByRole('heading', { level: 1, name: route.title })).toBeVisible();
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edgeSurfaces);
          if (layout.touch) await expectTouchTargets(page);
        });
      }
    });
  }
});
