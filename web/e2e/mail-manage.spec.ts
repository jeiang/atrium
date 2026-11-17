import type { Page } from '@playwright/test';
import {
  expectNoBlue,
  expectNoHorizontalScroll,
  expectNoSeriousAxeFindings,
  expectRoundedSurfaces,
  expectTouchTargets,
  gotoShell,
} from './checks.ts';
import { expect, goUnreachable, layouts, test, unreachableConsole, useScenario } from './fixtures.ts';

const edges = ['.rail', '.tabbar', 'dialog.sheet', 'dialog.dialog'];

function writes(page: Page): string[] {
  const sent: string[] = [];
  page.on('request', (request) => {
    if (request.method() === 'GET') return;
    const path = new URL(request.url()).pathname.replace('/web/v1', '');
    if (/^\/mail\/(status|rules(?!\/preview))/.test(path)) sent.push(`${request.method()} ${path}`);
  });
  return sent;
}

function toast(page: Page, text: string) {
  return page.locator('.toasts .toast', { hasText: text });
}

function rules(page: Page) {
  return page.getByRole('list', { name: 'Rules', exact: true });
}

test.describe('Mail rules and decisions', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('rules read as plain sentences without jargon', async ({ page }) => {
    await gotoShell(page, '/apps/mail/rules');
    await expect(page.getByRole('heading', { level: 1, name: 'Rules' })).toBeVisible();
    await expect(rules(page).getByRole('article')).toHaveCount(4);
    await expect(rules(page)).toContainText(
      'Mail where From domain is or ends in domain meadow-water.example or hillside-power.example goes to Bills.',
    );
    await expect(rules(page)).toContainText('Mail where List-Id is present goes to Newsletters.');
    await expect(rules(page)).toContainText(
      'Mail to home where Subject is one of Flash sale or Last chance, and Age in days is at least 30 is deleted.',
    );
    await expect(page.locator('main')).not.toContainText(/kNN|router|9B/);
  });

  test('a rule lists its decisions page by page, and each decision links back to its rule', async ({ page }) => {
    await gotoShell(page, '/apps/mail/rules');
    await rules(page).getByRole('link', { name: 'File into Bills' }).click();
    await expect(page).toHaveURL(/\/apps\/mail\/rules\/bills$/);
    const list = page.getByRole('list', { name: 'Decisions it made' });
    await expect(list.getByRole('article')).toHaveCount(20);
    await expect(page.getByText('24 decisions, newest first')).toBeVisible();
    await page.getByRole('button', { name: 'Show more' }).click();
    await expect(list.getByRole('article')).toHaveCount(24);
    await expect(page.getByRole('button', { name: 'Show more' })).toHaveCount(0);

    await gotoShell(page, '/apps/mail/decisions');
    const decisions = page.getByRole('list', { name: 'Decisions' });
    const changed = decisions.getByRole('article').filter({ hasText: 'Spring catalog is here' });
    await expect(changed).toContainText('Rule changed');
    await expect(changed).toContainText('You moved it to Shopping');
    const removed = decisions.getByRole('article').filter({ hasText: 'Raffle tickets for the harvest fair' });
    await expect(removed).toContainText('Rule removed');
    await expect(removed.getByRole('link', { name: /^Rule/ })).toHaveCount(0);
    const newsletter = decisions.getByRole('article').filter({ hasText: 'Compost workshop notes' });
    await newsletter.getByRole('link', { name: 'Rule: File into Newsletters' }).click();
    await expect(page).toHaveURL(/\/apps\/mail\/rules\/newsletters$/);
    await expect(page.locator('.mail-rule__sentence--lead')).toHaveText(
      'Mail where List-Id is present goes to Newsletters.',
    );
    await expect(page.getByRole('list', { name: 'Decisions it made' }).getByRole('article')).toHaveCount(3);
  });

  test('editing a rule shows before and after and saves without Undo', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/rules/newsletters');
    await page.getByRole('button', { name: 'Edit rule' }).click();
    const compare = page.getByRole('region', { name: 'Before and after' });
    await page.getByLabel('Folder').fill('Club news');
    await expect(compare).toContainText('Mail where List-Id is present goes to Newsletters.');
    await expect(compare).toContainText('Mail where List-Id is present goes to Club news.');
    await page.getByRole('button', { name: 'Preview matches' }).click();
    await expect(compare.getByRole('status')).toContainText('Matches 7 messages of the last 500');
    await page.getByRole('button', { name: 'Save rule' }).click();
    const saved = toast(page, 'Rule saved.');
    await expect(saved).toBeVisible();
    await expect(saved.getByRole('button', { name: 'Undo' })).toHaveCount(0);
    await expect(page.locator('.mail-rule__sentence--lead')).toHaveText(
      'Mail where List-Id is present goes to Club news.',
    );
    expect(sent).toEqual(['PATCH /mail/rules/newsletters']);
  });

  test('deleting a rule asks first and leaves its decisions marked removed', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/rules/newsletters');
    await page.getByRole('button', { name: 'Delete rule' }).click();
    const dialog = page.getByRole('dialog', { name: 'Delete this rule?' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    expect(sent).toEqual([]);
    await page.getByRole('button', { name: 'Delete rule' }).click();
    await dialog.getByRole('button', { name: 'Delete rule' }).click();
    await expect(toast(page, 'Rule deleted.')).toBeVisible();
    await expect(page.getByText('Rule newsletters was deleted.')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Decisions it made' }).getByRole('article')).toHaveCount(3);
    expect(sent).toEqual(['DELETE /mail/rules/newsletters']);
  });

  test('a decision is corrected from the Decisions screen and shows the new folder', async ({ page }) => {
    await gotoShell(page, '/apps/mail/decisions');
    const list = page.getByRole('list', { name: 'Decisions' });
    const first = list.getByRole('article').first();
    const subject = (await first.getByRole('link').first().textContent()) ?? '';
    await first.getByRole('button', { name: `Correct ${subject}` }).click();
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await expect(dialog).toContainText(subject);
    await expect(dialog).toContainText('The message stays where it is.');
    await dialog.getByRole('combobox', { name: 'Where should this go?' }).selectOption('Receipts');
    await dialog.getByRole('button', { name: 'Correct', exact: true }).click();
    await expect(toast(page, 'Corrected to Receipts.')).toBeVisible();
    await expect(list.getByRole('article').first()).toContainText('You moved it to Receipts');
  });

  test('rules show their order, say the first match wins, and move by keyboard', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/rules');
    await expect(page.getByText('The first rule that matches a message wins')).toHaveCount(1);
    const headings = rules(page).getByRole('link');
    await expect(headings).toHaveCount(4);
    const [one = '', two = ''] = await headings.allTextContents();
    const first = rules(page).getByRole('article').first();
    await expect(first.getByRole('button', { name: /up$/ })).toBeDisabled();
    await expect(rules(page).getByRole('article').last().getByRole('button', { name: /down$/ })).toBeDisabled();
    await first.getByRole('button', { name: /down$/ }).focus();
    await page.keyboard.press('Enter');
    await expect(toast(page, 'is now rule 2.')).toBeVisible();
    await expect(headings.nth(0)).toHaveText(two);
    await expect(headings.nth(1)).toHaveText(one);
    await expect(rules(page).getByRole('article').nth(1).getByRole('button', { name: /down$/ })).toBeFocused();
    await rules(page).getByRole('article').nth(1).getByRole('button', { name: /up$/ }).focus();
    await page.keyboard.press('Enter');
    await expect(headings.nth(0)).toHaveText(one);
    await expect(rules(page).getByRole('article').first().getByRole('button', { name: /down$/ })).toBeFocused();
    expect(sent).toEqual(['PUT /mail/rules/order', 'PUT /mail/rules/order']);
  });
});

test.describe('Mail mode', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409] });

  test('go-live shows the effect and needs the second step; Cancel sends nothing', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/mode');
    await expect(page.getByText('Mail is in Shadow mode.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open the pause' })).toHaveAttribute('href', '/assistant/authority');
    await page.getByRole('button', { name: 'Go live' }).click();
    const dialog = page.getByRole('dialog', { name: 'Go live?' });
    await expect(dialog.getByText(/^Sets mail_mode to live\. Automatic filing/)).toBeVisible();
    await expect(dialog).toContainText('garden-club, home');
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    expect(sent).toEqual(['POST /mail/status/confirm']);
    await page.getByRole('button', { name: 'Go live' }).click();
    await dialog.getByRole('button', { name: 'Go live' }).click();
    await expect(toast(page, 'Mail is live.')).toBeVisible();
    await expect(page.getByText('Mail is in Live mode.')).toBeVisible();
    await expect(page.getByText(/^Watch closely until/)).toBeVisible();
    await expect(page.getByRole('list', { name: 'Decisions to check' }).getByRole('article')).not.toHaveCount(0);
    expect(sent).toEqual(['POST /mail/status/confirm', 'POST /mail/status/confirm', 'POST /mail/status/go-live']);
  });

  test('not eligible shows the reasons and no go-live button', async ({ page }) => {
    await useScenario(page, { mailManage: { status: 'ineligible' } });
    await gotoShell(page, '/apps/mail/mode');
    await expect(page.getByText('Go-live is not possible now')).toBeVisible();
    await expect(page.getByRole('list', { name: 'Why not' })).toContainText(
      'An earlier change to the mailbox is unresolved',
    );
    await expect(page.getByRole('button', { name: 'Go live' })).toHaveCount(0);
  });

  test('watch closely shows after a switch with the decisions to check', async ({ page }) => {
    await useScenario(page, { mailManage: { status: 'watch' } });
    await gotoShell(page, '/apps/mail/mode');
    await expect(page.getByText(/^Watch closely until/)).toBeVisible();
    await expect(page.locator('main')).toContainText(/The mode changed to\s+Live on/);
    const checks = page.getByRole('list', { name: 'Decisions to check' });
    await expect(checks).toContainText('Spring catalog is here');
    await expect(page.getByRole('link', { name: 'All decisions' })).toHaveAttribute('href', '/apps/mail/decisions');
  });

  test('a replay run shows progress and then its summary', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailManage: { delay: 600 } });
    await gotoShell(page, '/apps/mail/mode');
    await page.getByRole('button', { name: 'Run a replay' }).click();
    const dialog = page.getByRole('dialog', { name: 'Run a replay now?' });
    await expect(dialog).toContainText('writes nothing to any mailbox');
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Run the replay' }).click();
    await expect(dialog.getByText(/The replay is running/)).toBeVisible();
    await expect(toast(page, 'Replay finished.')).toBeVisible();
    await expect(page.locator('[data-replay]')).toContainText('1820 messages replayed, 37 would now be sorted');
    expect(sent).toEqual(['POST /mail/status/replay']);
  });

  test('an expired confirmation is refused and must start again', async ({ page }) => {
    await useScenario(page, { mailManage: { confirm: 'stale' } });
    await gotoShell(page, '/apps/mail/mode');
    await page.getByRole('button', { name: 'Retake the baseline' }).click();
    const dialog = page.getByRole('dialog', { name: 'Retake the baseline?' });
    await dialog.getByRole('button', { name: 'Retake the baseline' }).click();
    await expect(dialog.getByText('The confirmation expired')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Start again' })).toBeVisible();
  });

  test('keyboard only: the go-live confirm opens with Cancel focused and Escape sends nothing', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/mode');
    await page.getByRole('button', { name: 'Go live' }).focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Go live?' });
    await expect(dialog.getByText(/^Sets mail_mode to live/)).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Go live' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(dialog.getByText(/^Sets mail_mode to live/)).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    expect(sent.filter((line) => line.endsWith('/go-live'))).toEqual([]);
    await page.keyboard.press('Enter');
    await expect(dialog.getByText(/^Sets mail_mode to live/)).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await dialog.getByRole('button', { name: 'Go live' }).focus();
    await expect(dialog.getByRole('button', { name: 'Go live' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(toast(page, 'Mail is live.')).toBeVisible();
    expect(sent.filter((line) => line.endsWith('/go-live'))).toEqual(['POST /mail/status/go-live']);
  });
});

test.describe('Mail mode unknown outcome', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('an unknown go-live outcome is blocked until the status is checked', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailManage: { command: 'unknown' } });
    await gotoShell(page, '/apps/mail/mode');
    await page.getByRole('button', { name: 'Go live' }).click();
    const dialog = page.getByRole('dialog', { name: 'Go live?' });
    await dialog.getByRole('button', { name: 'Go live' }).click();
    await expect(page.getByText('The outcome is unknown')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Go live' })).toHaveAttribute('aria-disabled', 'true');
    expect(sent).toEqual(['POST /mail/status/confirm', 'POST /mail/status/go-live']);
    await page.getByRole('button', { name: 'Check the status' }).click();
    await expect(page.getByText('The outcome is unknown')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Go live' })).not.toHaveAttribute('aria-disabled', 'true');
  });

  test('an unknown move is sent once and blocks moves until the order is checked', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailManage: { command: 'unknown' } });
    await gotoShell(page, '/apps/mail/rules');
    const list = rules(page);
    await expect(list.getByRole('link')).toHaveCount(4);
    const [, two = ''] = await list.getByRole('link').allTextContents();
    await list.getByRole('article').first().getByRole('button', { name: /down$/ }).click();
    await expect(page.getByText('The outcome is unknown')).toBeVisible();
    for (const button of await list.getByRole('button').all()) await expect(button).toBeDisabled();
    expect(sent).toEqual(['PUT /mail/rules/order']);
    await page.getByRole('button', { name: 'Check the order' }).click();
    await expect(page.getByText('The outcome is unknown')).toHaveCount(0);
    await expect(list.getByRole('link').nth(0)).toHaveText(two);
    await expect(list.getByRole('article').nth(1).getByRole('button', { name: /up$/ })).toBeEnabled();
    expect(sent).toEqual(['PUT /mail/rules/order']);
  });

  test('an unknown correction from Decisions is sent once and blocks corrections until checked', async ({ page }) => {
    const sent: string[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST') sent.push(new URL(request.url()).pathname.replace('/web/v1', ''));
    });
    await useScenario(page, { mailManage: { command: 'unknown' } });
    await gotoShell(page, '/apps/mail/decisions');
    const list = page.getByRole('list', { name: 'Decisions' });
    await list
      .getByRole('button', { name: /^Correct / })
      .first()
      .click();
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await dialog.getByRole('combobox', { name: 'Where should this go?' }).selectOption('Receipts');
    await dialog.getByRole('button', { name: 'Correct', exact: true }).click();
    await expect(page.getByText("The correction's outcome is unknown")).toBeVisible();
    for (const button of await list.getByRole('button', { name: /^Correct / }).all())
      await expect(button).toBeDisabled();
    await page.getByRole('button', { name: 'Check the decisions' }).click();
    await expect(page.getByText("The correction's outcome is unknown")).toHaveCount(0);
    await expect(list.getByRole('button', { name: /^Correct / }).first()).toBeEnabled();
    expect(sent.filter((path) => path.endsWith('/correction'))).toHaveLength(1);
  });
});

test.describe('Mail accounts', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('lists each account with its state; host rows are read-only', async ({ page }) => {
    await gotoShell(page, '/apps/mail/accounts');
    const list = page.getByRole('list', { name: 'Mail accounts' });
    await expect(list.getByRole('article')).toHaveCount(5);
    const host = list.getByRole('article').filter({ hasText: 'Allotment society' });
    await expect(host).toContainText('Set by the host');
    await expect(host.getByRole('button')).toHaveCount(0);
    await expect(host.getByRole('link', { name: 'Allotment society' })).toHaveAttribute(
      'href',
      '/settings/connections/mail/allotment',
    );
    await expect(list.getByRole('article').filter({ hasText: 'Old club' })).toContainText('Disabled');
    await expect(list.getByRole('article').filter({ hasText: 'Seed shop' })).toContainText('Failing');
    await expect(list.getByRole('article').filter({ hasText: 'Seed shop' })).toContainText(
      'The server refused the password.',
    );
  });
});

test.describe('Mail management offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('offline turns the mode commands and rule edits off and sends nothing', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/mode');
    await expect(page.getByRole('button', { name: 'Go live' })).toBeVisible();
    await goUnreachable(page);
    for (const name of ['Go live', 'Retake the baseline', 'Run a replay']) {
      await expect(page.getByRole('button', { name })).toHaveAttribute('aria-disabled', 'true');
      await page.getByRole('button', { name }).click({ force: true });
    }
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await page.goto('/apps/mail/rules/bills');
    await expect(page.getByText('Cannot reach atrium').first()).toBeVisible();
    expect(sent).toEqual([]);
  });

  test('offline turns the Decisions Correct buttons off', async ({ page }) => {
    await gotoShell(page, '/apps/mail/decisions');
    const list = page.getByRole('list', { name: 'Decisions' });
    await expect(list.getByRole('button', { name: /^Correct / }).first()).toBeEnabled();
    await goUnreachable(page);
    for (const button of await list.getByRole('button', { name: /^Correct / }).all())
      await expect(button).toBeDisabled();
  });

  test('offline turns the rule order buttons off and sends nothing', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/rules');
    await expect(rules(page).getByRole('button', { name: /down$/ }).first()).toBeEnabled();
    await goUnreachable(page);
    for (const button of await rules(page).getByRole('button').all()) await expect(button).toBeDisabled();
    await rules(page).getByRole('button', { name: /down$/ }).first().click({ force: true });
    expect(sent).toEqual([]);
  });
});

test.describe('Mail management layouts', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({
        viewport: { width: layout.width, height: layout.height },
        hasTouch: layout.touch,
        allowStatus: [503],
      });

      for (const [name, path, ready] of [
        ['rules', '/apps/mail/rules', 'Mail where List-Id is present goes to Newsletters.'],
        ['decisions', '/apps/mail/decisions', 'Spring catalog is here'],
        ['a rule', '/apps/mail/rules/seed-catalog', 'Decisions it made'],
        ['accounts', '/apps/mail/accounts', 'Set by the host'],
      ] as const) {
        test(`${name} fits, rounds and passes axe`, async ({ page }) => {
          await gotoShell(page, path);
          await expect(page.getByText(ready).first()).toBeVisible();
          await expectNoSeriousAxeFindings(page);
          await expectNoHorizontalScroll(page);
          await expectRoundedSurfaces(page, edges);
          if (layout.touch) await expectTouchTargets(page);
          await expectNoBlue(page);
        });
      }

      test('the rule editor fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/mail/rules/seed-catalog');
        await page.getByRole('button', { name: 'Edit rule' }).click();
        await expect(page.getByRole('region', { name: 'Before and after' })).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
      });

      test('the watch-closely status fits, rounds and passes axe', async ({ page }) => {
        await useScenario(page, { mailManage: { status: 'watch' } });
        await gotoShell(page, '/apps/mail/mode');
        await expect(page.getByRole('list', { name: 'Decisions to check' })).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
      });

      test('the go-live confirm fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/mail/mode');
        await page.getByRole('button', { name: 'Go live' }).click();
        await expect(page.getByText(/^Sets mail_mode to live/)).toBeVisible();
        await page.waitForTimeout(400);
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoBlue(page);
      });

      test('not eligible fits, rounds and passes axe', async ({ page }) => {
        await useScenario(page, { mailManage: { status: 'ineligible' } });
        await gotoShell(page, '/apps/mail/mode');
        await expect(page.getByText('Go-live is not possible now')).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
      });

      test('a status that did not load says so', async ({ page }) => {
        await useScenario(page, { mailManage: { status: 'error' } });
        await gotoShell(page, '/apps/mail/mode');
        await expect(page.getByText('The mail status did not load')).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
      });
    });
  }
});
