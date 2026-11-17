import type { Effect } from '../../contract';
import type { Outcome } from './types';

export type Stuck = 'send' | 'forward' | 'undo';

export const stuckChoices: Record<Stuck, { reason: string; outcomes: readonly Outcome[] }> = {
  forward: {
    reason:
      'atrium lost track of this change while it ran, so it is unknown whether it happened. Check the result where it applies, then record what you found. atrium never runs it again on its own.',
    outcomes: [
      {
        value: 'applied',
        label: 'Mark as done',
        ask: 'Mark this change as done?',
        effect: 'atrium records that the change was made and does not run it again.',
        done: 'Marked as done.',
      },
      {
        value: 'not-applied',
        label: 'Mark as not done',
        ask: 'Mark this change as not done?',
        effect: 'atrium records that the change was not made. It does not try it again.',
        done: 'Marked as not done.',
      },
    ],
  },
  undo: {
    reason:
      'atrium lost track of the undo while it ran, so it is unknown whether the change was reversed. Check the result where it applies, then record what you found. atrium never retries it on its own.',
    outcomes: [
      {
        value: 'undone',
        label: 'Mark undo as done',
        ask: 'Mark the undo as done?',
        effect: 'atrium records that the change was reversed and does not reverse it again.',
        done: 'Undo marked as done.',
      },
      {
        value: 'not-undone',
        label: 'Mark undo as not done',
        ask: 'Mark the undo as not done?',
        effect: 'atrium records that the change is still in place. It does not try the undo again.',
        done: 'Undo marked as not done.',
      },
    ],
  },
  send: {
    reason:
      'The send was interrupted, so it is unknown whether this email went out. The Sent folder can be empty even when the email was delivered. Ask the recipient or check your webmail, then record what you found. atrium never sends it again on its own.',
    outcomes: [
      {
        value: 'sent',
        label: 'Mark as sent',
        ask: 'Mark this email as sent?',
        effect: 'atrium records it as sent and does not send it again.',
        done: 'Marked as sent.',
      },
      {
        value: 'draft',
        label: 'Return to drafts',
        ask: 'Return this email to drafts?',
        effect: 'Nothing is sent now. If you send it again and the first send arrived, the recipient gets it twice.',
        done: 'Returned to drafts.',
      },
    ],
  },
};

export function stuckOf(effects: readonly Effect[]): Stuck {
  return effects.some((effect) => effect.undo_blocked || (effect.blocked && effect.direction === 'undo'))
    ? 'undo'
    : 'forward';
}

const resolvedOutcomes: Record<string, string> = {
  applied: 'Marked as done',
  'not-applied': 'Marked as not done',
  partial: 'Marked as partly done',
  undone: 'Undo marked as done',
  'not-undone': 'Undo marked as not done',
};

export function resolutionOf(effects: readonly Effect[]): { outcome: string; ts: number } | undefined {
  const last = effects
    .flatMap((effect) => (effect.resolution ? [effect.resolution] : []))
    .sort((a, b) => b.ts - a.ts || b.id - a.id)[0];
  const outcome = last && resolvedOutcomes[last.outcome];
  return last && outcome ? { outcome, ts: last.ts } : undefined;
}
