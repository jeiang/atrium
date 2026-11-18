import type { Page } from '@playwright/test';
import type { MockScenario } from '../src/mocks/handlers.ts';
import { gotoShell } from './checks.ts';
import { expect, useScenario } from './fixtures.ts';

const API = '/web/v1';

export interface UnsafeCommand {
  method: string;
  path: string;
  open: string;
  scenario?: MockScenario;
  press: (page: Page) => Promise<void>;
}

function record(page: Page, command: UnsafeCommand): string[] {
  const sent: string[] = [];
  const pattern = new RegExp(`^${API}${command.path}$`);
  page.on('request', (request) => {
    if (request.method() === command.method && pattern.test(new URL(request.url()).pathname)) {
      sent.push(request.headers()['idempotency-key'] ?? '');
    }
  });
  return sent;
}

export async function pressWithFault(page: Page, command: UnsafeCommand, answer: string): Promise<string[]> {
  await useScenario(page, {
    ...command.scenario,
    fault: { method: command.method, path: command.path, answer },
  });
  const sent = record(page, command);
  await gotoShell(page, command.open);
  await command.press(page);
  await expect.poll(() => sent.length).toBe(1);
  return sent;
}

export async function expectNeverAgain(page: Page, sent: string[]): Promise<void> {
  await page.waitForTimeout(2_000);
  expect(sent).toHaveLength(1);
  await page.reload();
  await expect(page.getByRole('button', { name: 'Account: Operator' })).toBeVisible();
  await page.waitForTimeout(2_000);
  expect(sent).toHaveLength(1);
}

export async function expectLostAnswer(page: Page, command: UnsafeCommand): Promise<void> {
  const sent = await pressWithFault(page, command, 'lost');
  expect(sent[0]).toMatch(/^[0-9a-f-]{36}$/);
  await expect(page.getByText(/unknown/i).first()).toBeVisible();
  await expectNeverAgain(page, sent);
}
