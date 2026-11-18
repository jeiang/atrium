import type { Locator, Page } from '@playwright/test';
import { expect, test, unreachableConsole } from './fixtures.ts';
import { expectLostAnswer, expectNeverAgain, pressWithFault, type UnsafeCommand } from './lost.ts';

const packagesOn = { apps: { on: ['packages'] } };

interface Command extends UnsafeCommand {
  name: string;
  unknown?: string;
  choices?: string[];
}

function dialog(page: Page): Locator {
  return page.locator('dialog[open]').last();
}

function toast(page: Page, text: string): Locator {
  return page.locator('.toasts .toast', { hasText: text });
}

async function approval(page: Page, key: string, name: string): Promise<void> {
  const item = page.locator(`[data-approval="${key}"]`);
  await expect(item.locator('.approval__row')).not.toHaveAttribute('inert');
  await item.getByRole('button', { name }).click();
}

async function packageButton(page: Page, id: number, name: string): Promise<void> {
  await page.locator(`li[data-package="${id}"]`).getByRole('button', { name }).click();
}

async function tokenRevoke(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Revoke Garden sensor bridge' }).click();
  await dialog(page).getByRole('button', { name: 'Revoke token' }).click();
}

const commands: Command[] = [
  {
    name: 'approve',
    method: 'POST',
    path: '/approvals/14/approve',
    open: '/inbox/proposal:14',
    press: (page) => approval(page, 'proposal:14', 'Approve'),
    unknown: 'effect_unknown',
  },
  {
    name: 'decline',
    method: 'POST',
    path: '/approvals/12/deny',
    open: '/inbox/proposal:12',
    press: (page) => approval(page, 'proposal:12', 'Decline'),
  },
  {
    name: 'email send',
    method: 'POST',
    path: '/drafts/\\d+/send',
    open: '/apps/mail',
    clock: true,
    press: async (page) => {
      await page.getByRole('button', { name: 'New email' }).click();
      const compose = page.locator('dialog[open]').first();
      await compose.getByLabel('To', { exact: true }).fill('Odile Marsh <odile@seedswap.example>');
      await compose.getByLabel('Subject').fill('Bean seeds');
      await compose.getByLabel('Message').fill('I will bring two jars.');
      await compose.getByRole('button', { name: 'Send', exact: true }).click();
      await expect(compose.locator('.receipt')).toContainText('in 10 s');
      await page.clock.runFor(10_500);
    },
    unknown: 'send_outcome_unknown',
    choices: ['Mark as sent'],
  },
  {
    name: 'mail flag',
    method: 'POST',
    path: '/mail/messages/103/flag',
    open: '/apps/mail/message/103',
    press: (page) => page.getByRole('button', { name: 'Flag', exact: true }).click(),
    unknown: 'write_outcome_unknown',
    choices: ['Mark as done'],
  },
  {
    name: 'mail archive',
    method: 'POST',
    path: '/mail/messages/103/archive',
    open: '/apps/mail/message/103',
    press: (page) => page.getByRole('button', { name: 'Archive' }).click(),
    unknown: 'write_outcome_unknown',
    choices: ['Mark as done'],
  },
  {
    name: 'mail read',
    method: 'POST',
    path: '/mail/messages/102/read',
    open: '/apps/mail/message/102',
    press: (page) => page.getByRole('button', { name: 'Mark as unread' }).click(),
    unknown: 'write_outcome_unknown',
    choices: ['Mark as done'],
  },
  {
    name: 'go-live',
    method: 'POST',
    path: '/mail/status/go-live',
    open: '/apps/mail/mode',
    press: async (page) => {
      await page.getByRole('button', { name: 'Go live' }).click();
      await dialog(page).getByRole('button', { name: 'Go live' }).click();
    },
    unknown: 'effect_unknown',
    choices: ['Check the status'],
  },
  {
    name: 'rebaseline',
    method: 'POST',
    path: '/mail/status/rebaseline',
    open: '/apps/mail/mode',
    press: async (page) => {
      await page.getByRole('button', { name: 'Retake the baseline' }).click();
      await dialog(page).getByRole('button', { name: 'Retake the baseline' }).click();
    },
    unknown: 'effect_unknown',
    choices: ['Check the status'],
  },
  {
    name: 'replay',
    method: 'POST',
    path: '/mail/status/replay',
    open: '/apps/mail/mode',
    press: async (page) => {
      await page.getByRole('button', { name: 'Run a replay' }).click();
      await dialog(page).getByRole('button', { name: 'Run the replay' }).click();
    },
    unknown: 'effect_unknown',
    choices: ['Check the status'],
  },
  {
    name: 'package add',
    method: 'POST',
    path: '/packages',
    open: '/apps/packages',
    scenario: packagesOn,
    press: async (page) => {
      const panel = page.getByRole('region', { name: 'Add a package' });
      await panel.getByLabel('Tracking number').fill('ZX9988776655');
      await panel.getByRole('button', { name: 'Add', exact: true }).click();
    },
  },
  {
    name: 'package track',
    method: 'POST',
    path: '/packages/2/track',
    open: '/apps/packages',
    scenario: packagesOn,
    press: async (page) => {
      await packageButton(page, 2, 'Track Lark and Loom 9400111202555842761234');
      await dialog(page).getByRole('button', { name: 'Track', exact: true }).click();
    },
    unknown: 'effect_unknown',
  },
  {
    name: 'package untrack',
    method: 'POST',
    path: '/packages/1/untrack',
    open: '/apps/packages',
    scenario: packagesOn,
    press: (page) => packageButton(page, 1, 'Stop tracking Juniper Outfitters 1Z999AA10123456784'),
    unknown: 'effect_unknown',
  },
  {
    name: 'package dismiss',
    method: 'POST',
    path: '/packages/5/dismiss',
    open: '/apps/packages',
    scenario: packagesOn,
    press: async (page) => {
      await packageButton(page, 5, 'Dismiss LP00123456789CN');
      await dialog(page).getByRole('button', { name: 'Dismiss', exact: true }).click();
    },
  },
  {
    name: 'package check',
    method: 'POST',
    path: '/packages/poll',
    open: '/apps/packages',
    scenario: packagesOn,
    press: (page) => page.getByRole('button', { name: 'Check now' }).click(),
  },
  {
    name: 'torrent pause',
    method: 'POST',
    path: '/media/torrents/a1/control',
    open: '/apps/torrents',
    press: (page) =>
      page
        .locator('li[data-torrent="a1"]')
        .getByRole('button', { name: /^Pause/ })
        .click(),
    unknown: 'effect_unknown',
  },
  {
    name: 'library file',
    method: 'POST',
    path: '/media/filings/[^/]+',
    open: '/apps/media/intake',
    press: async (page) => {
      const item = page
        .getByRole('list', { name: 'Waiting to be filed' })
        .getByRole('listitem')
        .filter({ hasText: 'Lantern.Street.S01E03.720p' })
        .first();
      await item.getByRole('button', { name: 'Show plan' }).click();
      await item.getByRole('button', { name: 'File Lantern.Street.S01E03.720p' }).click();
    },
    unknown: 'effect_unknown',
  },
  {
    name: 'library ignore',
    method: 'POST',
    path: '/media/filings/[^/]+/ignore',
    open: '/apps/media/intake',
    press: (page) => page.getByRole('button', { name: 'Ignore Paper.Boats.Extras' }).click(),
  },
  {
    name: 'library follow',
    method: 'POST',
    path: '/media/channels',
    open: '/apps/media/following',
    press: async (page) => {
      await page.getByLabel('Show name').fill('Cedar Workshop');
      await page.getByRole('button', { name: 'Follow', exact: true }).click();
    },
  },
  {
    name: 'library unfollow',
    method: 'DELETE',
    path: '/media/channels/[^/]+',
    open: '/apps/media/following',
    press: (page) => page.getByRole('button', { name: 'Unfollow Lantern Street' }).click(),
  },
  {
    name: 'library check',
    method: 'POST',
    path: '/media/channels/poll',
    open: '/apps/media/following',
    press: (page) => page.getByRole('button', { name: 'Check now' }).click(),
    unknown: 'effect_unknown',
  },
  {
    name: 'knowledge retract',
    method: 'POST',
    path: '/knowledge/21/retract',
    open: '/knowledge/21',
    press: (page) => page.getByRole('button', { name: 'Retract' }).click(),
  },
  {
    name: 'knowledge purge',
    method: 'DELETE',
    path: '/knowledge/21',
    open: '/knowledge/21',
    press: async (page) => {
      await page.getByRole('button', { name: 'Retract' }).click();
      await page.locator('.kpage').getByRole('button', { name: 'Delete for good' }).click();
      await dialog(page).getByRole('button', { name: 'Delete for good' }).click();
    },
  },
  {
    name: 'token make',
    method: 'POST',
    path: '/auth/tokens',
    open: '/settings/sign-in',
    press: async (page) => {
      await page.getByRole('button', { name: 'New token' }).click();
      await dialog(page).getByLabel('Name').fill('Kitchen tablet');
      await dialog(page).getByRole('button', { name: 'Make the token' }).click();
    },
  },
  {
    name: 'token revoke',
    method: 'DELETE',
    path: '/auth/tokens/2',
    open: '/settings/sign-in',
    press: tokenRevoke,
  },
  {
    name: 'connection save',
    method: 'PUT',
    path: '/connections/service/ship24',
    open: '/settings/connections/service/ship24',
    press: async (page) => {
      const fields = page.locator('#connection-fields');
      await fields.getByRole('textbox', { name: 'Name' }).fill('Parcel tracker');
      await fields.getByRole('button', { name: 'Test and save' }).click();
    },
  },
  {
    name: 'connection disconnect',
    method: 'DELETE',
    path: '/connections/service/ship24',
    open: '/settings/connections/service/ship24',
    press: async (page) => {
      await page.getByRole('button', { name: 'Disconnect Ship24 parcels' }).click();
      await dialog(page).getByRole('button', { name: 'Disconnect', exact: true }).click();
    },
  },
  {
    name: 'settings save',
    method: 'PATCH',
    path: '/settings',
    open: '/settings/general',
    press: async (page) => {
      await page.getByLabel('Time zone').fill('Europe/London');
      await page.getByRole('button', { name: 'Save' }).click();
    },
  },
  {
    name: 'chat stop',
    method: 'POST',
    path: '/threads/50/stop',
    open: '/chat/50',
    scenario: { chat: { speed: 4 } },
    press: (page) => page.getByRole('button', { name: 'Stop run' }).click(),
  },
  {
    name: 'chat edit',
    method: 'PATCH',
    path: '/threads/11/messages/latest',
    open: '/chat/11',
    scenario: { chat: { speed: 20 } },
    press: async (page) => {
      await page.getByRole('button', { name: 'Edit', exact: true }).click();
      await page.getByRole('textbox', { name: 'Edit your last message' }).fill('Does the roof need four sheets?');
      await page.getByRole('button', { name: 'Send edit (new run)' }).click();
    },
  },
  {
    name: 'undo',
    method: 'POST',
    path: '/effects/[^/]+/undo',
    open: '/apps/packages',
    scenario: packagesOn,
    press: async (page) => {
      await packageButton(page, 1, 'Stop tracking Juniper Outfitters 1Z999AA10123456784');
      await toast(page, 'Stopped tracking').getByRole('button', { name: 'Undo' }).click();
    },
  },
  {
    name: 'mail rule delete',
    method: 'DELETE',
    path: '/mail/rules/newsletters',
    open: '/apps/mail/rules/newsletters',
    press: async (page) => {
      await page.getByRole('button', { name: 'Delete rule' }).click();
      await dialog(page).getByRole('button', { name: 'Delete rule' }).click();
    },
  },
  {
    name: 'automation pause',
    method: 'POST',
    path: '/automation/pause',
    open: '/assistant/authority',
    press: (page) => page.getByRole('button', { name: 'Pause automation' }).click(),
  },
  {
    name: 'authority save',
    method: 'PUT',
    path: '/permissions',
    open: '/assistant/authority',
    press: async (page) => {
      await page.locator('[data-key="permission_internet_clean"]').getByRole('combobox').selectOption('deny');
      await page.getByRole('button', { name: 'Save authority' }).click();
    },
  },
  {
    name: 'assistant defaults save',
    method: 'PUT',
    path: '/assistant/defaults',
    open: '/assistant/behavior',
    press: async (page) => {
      const form = page.locator('[data-settings="defaults"]');
      await form.getByLabel('Effort').selectOption('max');
      await form.getByRole('button', { name: 'Save defaults' }).click();
    },
  },
  {
    name: 'routine run now',
    method: 'POST',
    path: '/schedules/1/run',
    open: '/assistant/routines/1',
    press: (page) => page.getByRole('button', { name: 'Run now' }).click(),
  },
  {
    name: 'app switch',
    method: 'PUT',
    path: '/apps/[^/]+/enabled',
    open: '/apps/directory',
    press: (page) => page.getByRole('switch', { name: /^Server ops(\s|$)/ }).click(),
  },
  {
    name: 'backup now',
    method: 'POST',
    path: '/maintenance/backups',
    open: '/settings/backups',
    press: (page) => page.getByRole('button', { name: 'Back up now' }).click(),
  },
];

test.describe('Unsafe commands', () => {
  test.use({ viewport: { width: 1440, height: 900 }, allowStatus: [409], allowConsole: unreachableConsole });

  for (const command of commands) {
    test(`${command.name}: a lost answer reads as unknown, is sent once and never again, even after a reload`, async ({
      page,
    }) => {
      await expectLostAnswer(page, command);
    });

    const unknown = command.unknown;
    if (unknown) {
      test(`${command.name}: ${unknown} shows as blocked with its choices and is never sent again`, async ({
        page,
      }) => {
        const sent = await pressWithFault(page, command, unknown);
        await expect(page.getByText(/unknown/i).first()).toBeVisible();
        for (const choice of command.choices ?? []) {
          await expect(page.getByRole('button', { name: choice }).first()).toBeVisible();
        }
        await expectNeverAgain(page, sent);
      });
    }
  }
});
