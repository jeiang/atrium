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
    if (/^\/(mail|drafts|undo|effects)/.test(path)) sent.push(`${request.method()} ${path}`);
  });
  return sent;
}

function toast(page: Page, text: string) {
  return page.locator('.toasts .toast', { hasText: text });
}

function compose(page: Page) {
  return page.locator('dialog[open]').first();
}

async function holdDown(page: Page, name: string): Promise<void> {
  const button = page.getByRole('button', { name });
  await button.scrollIntoViewIfNeeded();
  const box = await button.boundingBox();
  if (!box) throw new Error(`${name} has no box`);
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(1300);
  await page.mouse.up();
}

async function newEmail(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'New email' }).click();
  const dialog = compose(page);
  await dialog.getByLabel('To', { exact: true }).fill('Odile Marsh <odile@seedswap.example>');
  await dialog.getByLabel('Subject').fill('Bean seeds');
  await dialog.getByLabel('Message').fill('I will bring two jars.');
}

test.describe('Mail messages', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409] });

  test('lists messages as cards or rows with their marks and filters them', async ({ page }) => {
    await gotoShell(page, '/apps/mail');
    await expect(page.getByRole('heading', { level: 1, name: 'Mail' })).toBeVisible();
    const list = page.getByRole('list', { name: 'Messages' });
    await expect(list.getByRole('article')).toHaveCount(5);
    const seed = list.getByRole('article').filter({ hasText: 'Seed swap on Saturday' });
    await expect(seed.getByRole('link', { name: 'Unread: Seed swap on Saturday' })).toBeVisible();
    await expect(seed).toContainText('Could you bring the bean seeds');
    const renewal = list.getByRole('article').filter({ hasText: 'Plot 14 renewal' });
    await expect(renewal).toContainText('Flagged');
    await expect(renewal).toContainText('Attachment');
    await page.getByText('List', { exact: true }).click();
    await expect(page.locator('.mail-list--list')).toBeVisible();
    await expect(seed).not.toContainText('Could you bring the bean seeds');
    await page.getByLabel('Search mail').fill('compost');
    await page.getByRole('button', { name: 'Search' }).click();
    await expect(list.getByRole('article')).toHaveCount(1);
    await page.getByRole('button', { name: 'Clear' }).click();
    await page.getByRole('combobox', { name: 'Account' }).selectOption('home');
    await page.getByRole('button', { name: 'Search' }).click();
    await expect(list.getByRole('article')).toHaveCount(1);
    await expect(list).toContainText('Your order has shipped');
    await page.getByRole('button', { name: 'Clear' }).click();
    const folder = page.getByRole('combobox', { name: 'Folder' });
    await expect(folder).toBeDisabled();
    await page.getByRole('combobox', { name: 'Account' }).selectOption('garden-club');
    await expect(folder).toBeEnabled();
    await expect(folder.locator('option')).toContainText(['Any folder', 'Archive', 'Bills', 'INBOX']);
    await folder.selectOption('Bills');
    await page.getByRole('button', { name: 'Search' }).click();
    await expect(list.getByRole('article')).toHaveCount(0);
    await page.getByRole('combobox', { name: 'Account' }).selectOption('');
    await expect(folder).toHaveValue('');
  });

  test('the message page shows the text and attachments, and read, flag and archive offer Undo', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail');
    await page.getByRole('link', { name: 'Plot 14 renewal' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Plot 14 renewal' })).toBeVisible();
    await expect(page.locator('.mail-body')).toHaveText(/Your plot renewal is due/);
    const files = page.getByRole('region', { name: 'Attachments' });
    await expect(files.getByRole('listitem')).toHaveCount(2);
    await expect(files).toContainText('renewal-form.pdf');
    await expect(files).toContainText('application/pdf, 180 KB');

    await page.getByRole('button', { name: 'Mark as unread' }).click();
    await expect(toast(page, 'Marked as unread.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Mark as read' })).toBeVisible();
    await page.getByRole('button', { name: 'Remove flag' }).click();
    await expect(toast(page, 'Flag removed.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Flag', exact: true })).toBeVisible();
    await toast(page, 'Flag removed.').getByRole('button', { name: 'Undo' }).click();
    await expect(toast(page, 'Flagged again.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Remove flag' })).toBeVisible();

    await page.getByRole('button', { name: 'Archive' }).click();
    await expect(toast(page, 'Archived.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Archive' })).toHaveCount(0);
    await toast(page, 'Archived.').getByRole('button', { name: 'Undo' }).click();
    await expect(toast(page, 'Moved back to the inbox.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Archive' })).toBeVisible();
    expect(sent).toEqual([
      'POST /mail/messages/102/read',
      'POST /mail/messages/102/flag',
      'POST /undo/write:5001',
      'POST /mail/messages/102/archive',
      'POST /undo/write:5002',
    ]);
  });

  test('a message with an open loop links to its Inbox item', async ({ page }) => {
    await gotoShell(page, '/apps/mail/message/101');
    await expect(page.getByRole('link', { name: 'Open in Inbox' })).toHaveAttribute(
      'href',
      /\/inbox\/loop%3Athread-seed-swap$/,
    );
    await gotoShell(page, '/apps/mail/message/103');
    await expect(page.locator('.mail-body')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Open in Inbox' })).toHaveCount(0);
  });

  test('reply and forward open filled-in drafts', async ({ page }) => {
    await gotoShell(page, '/apps/mail/message/101');
    await page.getByRole('button', { name: 'Reply' }).click();
    const reply = compose(page);
    await expect(reply.getByRole('heading', { name: 'Reply' })).toBeVisible();
    await expect(reply.getByLabel('To', { exact: true })).toHaveValue('Odile Marsh <odile@seedswap.example>');
    await expect(reply.getByLabel('Subject')).toHaveValue('Re: Seed swap on Saturday');
    await expect(reply.getByLabel('Message')).toHaveValue(/> Could you bring the bean seeds/);
    await reply.getByRole('button', { name: 'Discard' }).click();
    await expect(toast(page, 'Draft discarded.')).toBeVisible();

    await page.getByRole('button', { name: 'Forward' }).click();
    const forward = compose(page);
    await expect(forward.getByRole('heading', { name: 'Forward' })).toBeVisible();
    await expect(forward.getByLabel('Message')).toHaveCount(0);
    await forward.getByLabel('To', { exact: true }).fill('teo@lindqvist.example');
    await forward.getByRole('button', { name: 'Continue' }).click();
    await expect(forward.getByLabel('Subject')).toHaveValue('Fwd: Seed swap on Saturday');
    await expect(forward.getByLabel('Message')).toHaveValue(/---------- Forwarded message ---------/);
    await expect(forward.getByLabel('Message')).toHaveValue(/Could you bring the bean seeds/);
  });

  test('send waits 10 seconds: Undo sends nothing, letting it run sends once', async ({ page }) => {
    await page.clock.install();
    const sent = writes(page);
    await gotoShell(page, '/apps/mail');
    await newEmail(page);
    const dialog = compose(page);
    await dialog.getByRole('button', { name: 'Send', exact: true }).click();
    await expect(dialog.locator('.receipt')).toContainText('Sending to Odile Marsh in 10 s');
    await page.clock.runFor(4_000);
    await dialog.locator('.receipt').getByRole('button', { name: 'Undo' }).click();
    await expect(dialog.locator('.receipt')).toHaveCount(0);
    await page.clock.runFor(12_000);
    expect(sent.filter((line) => line.endsWith('/send'))).toEqual([]);

    await dialog.getByRole('button', { name: 'Send', exact: true }).click();
    await expect(dialog.locator('.receipt')).toContainText('in 10 s');
    await page.clock.runFor(10_500);
    await expect(toast(page, 'Sent to Odile Marsh.')).toBeVisible();
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    expect(sent).toEqual(['POST /drafts', 'POST /drafts/500/send']);
  });

  test('an unknown send is shown as blocked and can be recorded as sent', async ({ page }) => {
    await useScenario(page, { mailbox: { send: 'unknown' } });
    const sent = writes(page);
    await page.clock.install();
    await gotoShell(page, '/apps/mail');
    await newEmail(page);
    const dialog = compose(page);
    await dialog.getByRole('button', { name: 'Send', exact: true }).click();
    await expect(dialog.locator('.receipt')).toContainText('in 10 s');
    await page.clock.runFor(10_500);
    await expect(dialog.getByText('The send outcome is unknown')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Send', exact: true })).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Mark as sent' }).click();
    const ask = page.getByRole('dialog', { name: 'Mark this email as sent?' });
    await expect(ask.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await ask.getByRole('button', { name: 'Mark as sent' }).click();
    await expect(toast(page, 'Marked as sent.')).toBeVisible();
    expect(sent).toEqual(['POST /drafts', 'POST /drafts/500/send', 'POST /drafts/500/resolve']);
  });
});

test.describe('Mail refusals', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409] });

  test('shadow mode refuses a flag and says why', async ({ page }) => {
    await useScenario(page, { mailbox: { refuse: 'shadow_mode' } });
    await gotoShell(page, '/apps/mail/message/103');
    await page.getByRole('button', { name: 'Flag', exact: true }).click();
    await expect(page.getByText('Mail is in shadow mode')).toBeVisible();
    await expect(page.getByText(/Switch Mail to live mode/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Flag', exact: true })).toBeVisible();
  });

  test('an unknown flag outcome blocks further changes until recorded', async ({ page }) => {
    await useScenario(page, { mailbox: { action: 'unknown' } });
    await gotoShell(page, '/apps/mail/message/103');
    await page.getByRole('button', { name: 'Flag', exact: true }).click();
    await expect(page.getByText('The outcome is unknown')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Archive' })).toBeDisabled();
    await page.getByRole('button', { name: 'Mark as done' }).click();
    await page
      .getByRole('dialog', { name: 'Mark this change as done?' })
      .getByRole('button', { name: 'Mark as done' })
      .click();
    await expect(toast(page, 'Marked as done.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Archive' })).toBeEnabled();
  });
});

test.describe('Mail corrections', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [422], allowConsole: unreachableConsole });

  async function correctTo(page: Page, folder: string): Promise<void> {
    await page.getByRole('button', { name: 'Correct', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await dialog.getByRole('combobox', { name: 'Where should this go?' }).selectOption(folder);
    await dialog.getByRole('button', { name: 'Correct', exact: true }).click();
  }

  test('live mode moves the message once and offers Undo', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailManage: { status: 'live' } });
    await gotoShell(page, '/apps/mail/message/102');
    await page.getByRole('button', { name: 'Correct', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await expect(dialog).toContainText('Records the correction and moves the message to this folder.');
    await expect(dialog.getByRole('option', { name: 'INBOX' })).toHaveCount(0);
    await dialog.getByRole('combobox', { name: 'Where should this go?' }).selectOption('Receipts');
    await dialog.getByRole('button', { name: 'Correct', exact: true }).click();
    await expect(toast(page, 'Corrected to Receipts. The message was moved.')).toBeVisible();
    await toast(page, 'Corrected to Receipts.').getByRole('button', { name: 'Undo' }).click();
    await expect(toast(page, 'Moved back to where it was.')).toBeVisible();
    expect(sent).toEqual(['POST /mail/messages/102/correction', 'POST /undo/write:5000']);
  });

  test('shadow mode records the correction without Undo and preselects it next time', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/message/102');
    await page.getByRole('button', { name: 'Correct', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await expect(dialog).toContainText('The message stays where it is.');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await correctTo(page, 'Bills');
    await expect(toast(page, 'Corrected to Bills.')).toBeVisible();
    await expect(toast(page, 'Corrected to Bills.').getByRole('button', { name: 'Undo' })).toHaveCount(0);
    await page.getByRole('button', { name: 'Correct', exact: true }).click();
    await expect(dialog.getByRole('combobox', { name: 'Where should this go?' })).toHaveValue('Bills');
    expect(sent).toEqual(['POST /mail/messages/102/correction']);
  });

  test('a refusal stays in the dialog and says why', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailbox: { refuse: 'sent_message' } });
    await gotoShell(page, '/apps/mail/message/102');
    await correctTo(page, 'Bills');
    const dialog = page.getByRole('dialog', { name: 'Correct this decision' });
    await expect(dialog).toContainText('cannot correct a sent message Nothing was changed.');
    expect(sent).toEqual(['POST /mail/messages/102/correction']);
  });

  test('an unknown outcome is sent once and blocks correcting until the message is checked', async ({ page }) => {
    const sent = writes(page);
    await useScenario(page, { mailbox: { action: 'unknown' } });
    await gotoShell(page, '/apps/mail/message/102');
    await correctTo(page, 'Bills');
    await expect(page.getByText("The correction's outcome is unknown")).toBeVisible();
    await expect(page.getByRole('button', { name: 'Correct', exact: true })).toHaveAttribute('aria-disabled', 'true');
    expect(sent).toEqual(['POST /mail/messages/102/correction']);
    await page.getByRole('button', { name: 'Check the message' }).click();
    await expect(page.getByText("The correction's outcome is unknown")).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Correct', exact: true })).not.toHaveAttribute(
      'aria-disabled',
      'true',
    );
    expect(sent).toEqual(['POST /mail/messages/102/correction']);
  });

  test('offline turns Correct off and sends nothing', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/message/102');
    await expect(page.getByRole('button', { name: 'Correct', exact: true })).toBeVisible();
    await goUnreachable(page);
    await expect(page.getByRole('button', { name: 'Correct', exact: true })).toHaveAttribute('aria-disabled', 'true');
    await page.getByRole('button', { name: 'Correct', exact: true }).click({ force: true });
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect(sent).toEqual([]);
  });
});

test.describe('Mail offline', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowConsole: unreachableConsole });

  test('offline keeps the draft open and turns sending off', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail');
    await newEmail(page);
    await goUnreachable(page);
    const dialog = compose(page);
    await expect(dialog.getByText('Sending is off until atrium answers again.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Send', exact: true })).toHaveAttribute('aria-disabled', 'true');
    await expect(dialog.getByLabel('Message')).toHaveValue('I will bring two jars.');
    expect(sent).toEqual([]);
  });
});

test.describe('Mail on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });

  test('a reply sends with a press and hold', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail/message/101');
    await page.getByRole('button', { name: 'Reply' }).click();
    await expect(page.locator('dialog.sheet[open]')).toBeVisible();
    await page.getByRole('button', { name: 'Hold to send' }).click();
    expect(sent.filter((line) => line.endsWith('/send'))).toEqual([]);
    await holdDown(page, 'Hold to send');
    await expect(toast(page, 'Sent to Odile Marsh.')).toBeVisible();
    expect(sent).toEqual(['POST /mail/messages/101/reply', 'POST /drafts/500/send']);
  });

  test('keyboard only: compose, then Enter asks to confirm with Cancel focused', async ({ page }) => {
    const sent = writes(page);
    await gotoShell(page, '/apps/mail');
    await page.getByRole('button', { name: 'New email' }).focus();
    await page.keyboard.press('Enter');
    const dialog = compose(page);
    await dialog.getByLabel('To', { exact: true }).focus();
    await page.keyboard.type('mina@okafor.example');
    await dialog.getByLabel('Message').focus();
    await page.keyboard.type('Thanks for the rota.');
    await dialog.getByRole('button', { name: 'Hold to send' }).focus();
    await page.keyboard.press('Enter');
    const confirm = page.getByRole('dialog', { name: 'Send this now?' });
    await expect(confirm).toBeVisible();
    await expect(confirm.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(confirm).toBeHidden();
    expect(sent).toEqual([]);
    await dialog.getByRole('button', { name: 'Hold to send' }).focus();
    await page.keyboard.press('Enter');
    await confirm.getByRole('button', { name: 'Send now' }).focus();
    await page.keyboard.press('Enter');
    await expect(toast(page, 'Sent to mina@okafor.example.')).toBeVisible();
    expect(sent).toEqual(['POST /drafts', 'POST /drafts/500/send']);
  });
});

test.describe('Mail layouts', () => {
  for (const layout of layouts) {
    test.describe(layout.name, () => {
      test.use({
        viewport: { width: layout.width, height: layout.height },
        hasTouch: layout.touch,
        allowStatus: [409],
      });

      test('the message list fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/mail');
        await expect(page.getByRole('list', { name: 'Messages' }).getByRole('article')).toHaveCount(5);
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
        await page.getByText('List', { exact: true }).click();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
      });

      test('a message with a refusal fits, rounds and passes axe', async ({ page }) => {
        await useScenario(page, { mailbox: { refuse: 'shadow_mode' } });
        await gotoShell(page, '/apps/mail/message/102');
        await page.getByRole('button', { name: 'Archive' }).click();
        await expect(page.getByText('Mail is in shadow mode')).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page);
        await expectNoBlue(page);
      });

      test('the correct dialog fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/mail/message/102');
        await page.getByRole('button', { name: 'Correct', exact: true }).click();
        await expect(page.getByRole('combobox', { name: 'Where should this go?' })).toBeVisible();
        await page.waitForTimeout(400);
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoBlue(page);
      });

      test('compose fits, rounds and passes axe', async ({ page }) => {
        await gotoShell(page, '/apps/mail/message/101');
        await page.getByRole('button', { name: 'Reply' }).click();
        await expect(compose(page).getByLabel('Subject')).toHaveValue('Re: Seed swap on Saturday');
        await page.waitForTimeout(400);
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
        if (layout.touch) await expectTouchTargets(page, 'dialog[open]');
        await expectNoBlue(page);
      });

      test('an empty mailbox says so', async ({ page }) => {
        await useScenario(page, { mailbox: { messages: 'empty' } });
        await gotoShell(page, '/apps/mail');
        await expect(page.getByText('No messages yet')).toBeVisible();
        await expectNoSeriousAxeFindings(page);
        await expectNoHorizontalScroll(page);
        await expectRoundedSurfaces(page, edges);
      });
    });
  }
});
