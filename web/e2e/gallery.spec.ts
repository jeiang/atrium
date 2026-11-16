import type { Page } from '@playwright/test';
import {
  expectFocusVisible,
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectReducedMotion,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, galleryLayouts, layouts, test } from './fixtures.ts';

const edgeSurfaces = ['header.bar', 'dialog.sheet'];

async function gotoGallery(page: Page): Promise<void> {
  test.slow();
  await gotoShell(page, '/__gallery');
  await expect(page.getByRole('heading', { level: 1, name: 'Component gallery' })).toBeVisible({ timeout: 0 });
}

test.describe('gallery design checks', () => {
  for (const layout of galleryLayouts) {
    test.describe(layout.name, () => {
      test.use({ viewport: { width: layout.width, height: layout.height }, hasTouch: layout.touch });
      for (const scheme of ['light', 'dark'] as const) {
        test(`${scheme}: fits, rounds, has no blue${layout.touch ? ', meets the touch rule' : ''} and passes axe`, async ({
          page,
        }) => {
          await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
          await gotoGallery(page);
          await expect(page).toHaveTitle('Gallery · atrium');
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edgeSurfaces);
          await expectNoBlue(page);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoSeriousAxeFindings(page);
        });
      }

      const overlays = [
        { open: 'Rename thread', scope: 'dialog[open]', name: 'dialog' },
        { open: 'Open sheet', scope: 'dialog[open]', name: 'sheet' },
        { open: 'Show toast', scope: '.toasts', name: 'toast' },
      ];
      for (const overlay of overlays) {
        test(`the open ${overlay.name} passes the design checks`, async ({ page }) => {
          await page.emulateMedia({ reducedMotion: 'reduce' });
          await gotoGallery(page);
          await page.getByRole('button', { name: overlay.open }).click();
          await expect(page.locator(overlay.scope).first()).toBeVisible();
          await page.evaluate(() =>
            Promise.allSettled(document.getAnimations().map((animation) => animation.finished)),
          );
          await expectRoundedSurfaces(page, edgeSurfaces);
          await expectNoBlue(page);
          if (layout.touch) await expectTouchTargets(page, overlay.scope);
          await expectNoSeriousAxeFindings(page);
        });
      }
    });
  }
});

test.describe('reduced motion', () => {
  for (const layout of [layouts[0], layouts[2]].filter((item) => item !== undefined)) {
    test(`${layout.name}: only opacity moves`, async ({ page }) => {
      await page.setViewportSize({ width: layout.width, height: layout.height });
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await gotoGallery(page);
      await expectReducedMotion(page);
      await page.getByRole('button', { name: 'Open sheet' }).click();
      await expectReducedMotion(page);
      await page.keyboard.press('Escape');
      await page.getByRole('button', { name: 'Show toast' }).click();
      await expectReducedMotion(page);
      await page.getByRole('button', { name: 'Save', exact: true }).click();
      await expectReducedMotion(page);
    });
  }

  test('motion is allowed without the preference', async ({ page }) => {
    await gotoGallery(page);
    await page.evaluate(() => {
      const started: string[] = [];
      Reflect.set(window, 'animationsStarted', started);
      document.addEventListener('animationstart', (event) => started.push(event.animationName), { capture: true });
    });
    await page.getByRole('button', { name: 'Rename thread' }).click();
    await expect
      .poll(() => page.evaluate(() => Reflect.get(window, 'animationsStarted') as string[]))
      .toContain('rise');
  });
});

test.describe('keyboard focus', () => {
  test.use({ viewport: { width: 1440, height: 900 } });
  for (const section of ['buttons', 'links', 'fields', 'choices', 'cards', 'table', 'overlays', 'simulator']) {
    test(`every stop in ${section} shows the focus ring`, async ({ page }) => {
      await gotoGallery(page);
      await expectFocusVisible(page, `[data-gallery="${section}"]`);
    });
  }
});

test.describe('behavior', () => {
  test('a field reports its error after blur and clears it once valid', async ({ page }) => {
    await gotoGallery(page);
    const field = page.getByRole('textbox', { name: 'Reply address' });
    await field.fill('robin@');
    await expect(field).not.toHaveAttribute('aria-invalid', 'true');
    await field.blur();
    await expect(field).toHaveAttribute('aria-invalid', 'true');
    await expect(field).toHaveAccessibleDescription('Enter an address like name@example.test');
    await expect(page.locator('[role="status"][aria-live="polite"]')).toHaveText(
      'Enter an address like name@example.test',
    );
    await field.fill('robin@example.test');
    await expect(field).not.toHaveAttribute('aria-invalid', 'true');
  });

  test('a disabled button explains why', async ({ page }) => {
    await gotoGallery(page);
    const button = page.getByRole('button', { name: 'Send digest' });
    await expect(button).toHaveAttribute('aria-disabled', 'true');
    await expect(button).toHaveAccessibleDescription('Connect a mail account first');
  });

  test('a pending button keeps its label and ignores clicks', async ({ page }) => {
    await gotoGallery(page);
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const busy = page.getByRole('button', { name: 'Saving' });
    await expect(busy).toHaveAttribute('aria-busy', 'true');
    await busy.click();
    await expect(page.getByTestId('save-count')).toHaveText('Saved 1 times');
  });

  test('a dialog closes on Escape and returns focus', async ({ page }) => {
    await gotoGallery(page);
    const opener = page.getByRole('button', { name: 'Rename thread' });
    await opener.focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Rename thread' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('textbox', { name: 'Title' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(opener).toBeFocused();
  });

  test('a pending confirm blocks Escape until it finishes', async ({ page }) => {
    await gotoGallery(page);
    await page.getByRole('button', { name: 'Delete thread' }).click();
    const dialog = page.getByRole('dialog', { name: 'Delete this thread?' });
    await expect(dialog.getByRole('button', { name: 'Keep thread' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(dialog.getByRole('button', { name: 'Deleting' })).toHaveAttribute('aria-busy', 'true');
    await page.keyboard.press('Escape');
    await expect(dialog).toBeVisible();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('list', { name: 'Notifications' })).toContainText('Thread deleted');
  });

  test('the table sorts and moves by keyboard', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoGallery(page);
    const table = page.getByRole('table', { name: 'Inbox', exact: true });
    const received = table.getByRole('columnheader', { name: 'Received' });
    await expect(received).toHaveAttribute('aria-sort', 'descending');
    await table.getByRole('button', { name: 'From' }).click();
    await expect(table.getByRole('columnheader', { name: 'From' })).toHaveAttribute('aria-sort', 'ascending');
    const rows = table.locator('tbody tr');
    await expect(rows.first()).toContainText('Allotment society');
    await rows.first().focus();
    await page.keyboard.press('ArrowDown');
    await expect(rows.nth(1)).toBeFocused();
    await page.keyboard.press('End');
    await expect(rows.last()).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(rows.last()).toHaveAttribute('aria-current', 'true');
    await expect(page.getByTestId('opened')).toHaveText('Opened Appointment reminder');
  });

  test('the table becomes a list on a phone', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoGallery(page);
    await expect(page.getByRole('table', { name: 'Inbox', exact: true })).toBeHidden();
    await page.getByRole('list', { name: 'Inbox', exact: true }).getByRole('button').first().click();
    await expect(page.getByTestId('opened')).toHaveText('Opened Your hold is ready');
  });

  test('a toast announces itself and waits while hovered', async ({ page }) => {
    await page.clock.install();
    await gotoGallery(page);
    await page.getByRole('button', { name: 'Show toast' }).click();
    const toast = page.getByRole('listitem').filter({ hasText: 'Reminder saved' });
    await expect(toast).toBeVisible();
    await expect(page.locator('[role="status"][aria-live="polite"]')).toHaveText('Reminder saved');
    await toast.hover();
    await page.clock.runFor(11_000);
    await expect(toast).toBeVisible();
    await page.mouse.move(0, 0);
    await page.clock.runFor(11_000);
    await expect(toast).toBeHidden();
  });

  test('switches toggle, wait while pending and stay locked when set by host', async ({ page }) => {
    await gotoGallery(page);
    const digest = page.getByRole('switch', { name: 'Morning digest' });
    await expect(digest).toBeChecked();
    await digest.click();
    await expect(digest).not.toBeChecked();
    const locked = page.getByRole('switch', { name: 'Remote tools' });
    await expect(locked).toHaveAccessibleDescription('Set by host');
    await locked.click({ force: true });
    await expect(locked).toBeChecked();
    const quiet = page.getByRole('switch', { name: 'Quiet hours' });
    await quiet.click();
    await expect(quiet).toHaveAttribute('aria-busy', 'true');
    await expect(quiet).toBeChecked();
    await expect(quiet).not.toHaveAttribute('aria-busy');
  });

  test('the scheme control switches the theme', async ({ page }) => {
    await gotoGallery(page);
    const read = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const before = await read();
    await page.getByRole('group', { name: 'Color scheme' }).getByText('Dark').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await expect.poll(read).not.toBe(before);
  });
});

test.describe('run stream simulator', () => {
  test('plays each script to its end', async ({ page }) => {
    await gotoGallery(page);
    const section = page.locator('[data-gallery="simulator"]');
    await section.getByText('Instant').click();
    for (const [label, state] of [
      ['Finishes', 'done'],
      ['Fails', 'failed'],
      ['Parks', 'parked'],
    ] as const) {
      await section.getByText(label, { exact: true }).click();
      await section.getByRole('button', { name: 'Play' }).click();
      await expect(section.getByTestId('run-result')).toHaveText(state);
    }
    await expect(section.getByRole('list', { name: 'Run events' }).getByRole('listitem').last()).toContainText(
      '{"state":"parked"}',
    );
  });

  test('answers cursors and faults like the server', async ({ page, problems }) => {
    await gotoGallery(page);
    const results = await page.evaluate(async () => {
      const base = '/web/v1/threads/7/runs/101/events';
      const read = async (url: string, init?: RequestInit) => {
        const response = await fetch(url, init);
        return { status: response.status, body: await response.text() };
      };
      return {
        replay: await read(`${base}?speed=0`, { headers: { 'Last-Event-ID': '7' } }),
        after: await read(`${base}?speed=0&after=8`),
        finished: await read(`${base}?after=9`),
        beyond: await read(`${base}?after=10`),
        malformed: await read(`${base}?after=x`),
        unknown: await read('/web/v1/threads/7/runs/999/events'),
        otherThread: await read('/web/v1/threads/8/runs/101/events'),
        expired: await read(`${base}?fault=expired`),
        busy: await read(`${base}?fault=busy`),
        parked: await read('/web/v1/threads/8/runs/103/events?speed=0&after=3'),
      };
    });
    expect(results.replay.status).toBe(200);
    expect(results.replay.body.startsWith('retry: 1000\n\n')).toBe(true);
    expect(results.replay.body).toContain('id: 8\nevent: run_done');
    expect(results.replay.body).not.toContain('id: 7\n');
    expect(results.replay.body.trimEnd().endsWith('event: done\ndata: {"state":"done"}')).toBe(true);
    expect(results.after.body).toContain('id: 9\nevent: complete');
    expect(results.finished.status).toBe(204);
    expect(results.beyond.status).toBe(400);
    expect(results.malformed.status).toBe(400);
    expect(results.unknown.status).toBe(404);
    expect(results.otherThread.status).toBe(404);
    expect(results.expired.status).toBe(410);
    expect(JSON.parse(results.expired.body)).toMatchObject({ error: { code: 'stream_expired' }, run_id: 101 });
    expect(results.busy.status).toBe(429);
    expect(results.parked.status).toBe(200);
    expect(results.parked.body).toContain('data: {"state":"parked"}');
    expect(problems.every((line) => line.includes('Failed to load resource'))).toBe(true);
    problems.length = 0;
  });

  test('a dropped stream resumes from the last id', async ({ page }) => {
    await gotoGallery(page);
    const outcome = await page.evaluate(async () => {
      const base = '/web/v1/threads/7/runs/101/events?speed=0';
      const dropped = await (await fetch(`${base}&fault=drop:3`)).text();
      const ids = [...dropped.matchAll(/^id: (\d+)$/gm)].map((match) => Number(match[1]));
      const last = ids.at(-1) ?? 0;
      const resumed = await (await fetch(base, { headers: { 'Last-Event-ID': String(last) } })).text();
      return { finished: dropped.includes('event: done'), last, resumed };
    });
    expect(outcome.finished).toBe(false);
    expect(outcome.last).toBe(3);
    expect(outcome.resumed).toContain('id: 4\n');
    expect(outcome.resumed).toContain('event: complete');
  });
});
