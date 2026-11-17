import { createInfiniteQuery, createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type AppEntry,
  type ConfirmEffect,
  type Row as Decision,
  type MailCondition,
  type MailListedRule,
  type MailRuleInput,
  type MailStatus,
  mailGoLive,
  mailReplayRun,
  mailRuleCreate,
  mailRuleDecisions,
  mailRuleDelete,
  mailRulePreview,
  mailRules,
  mailRulesVocabulary,
  mailRuleUpdate,
  mailStatus,
  mailStatusConfirm,
  mailStatusRebaseline,
  mailTriage,
  ok,
  type Preview,
  type ReplayRun,
  type RuleRef,
  type RuleVocabulary,
} from '../../contract';
import { p } from '../../shell/router';

export type { ConfirmEffect, Decision, MailListedRule, MailStatus, Preview, ReplayRun, RuleRef, RuleVocabulary };

const TOPICS = ['mail'] as const;
const PAGE = 20;

export function mailEnabled(apps: AppEntry[] | undefined): boolean {
  return apps?.find((item) => item.id === 'mail')?.enabled !== false;
}

export interface DecisionFilter {
  q: string;
  account: string;
  agreement: string;
}

export function useDecisions(filter: () => DecisionFilter, enabled: () => boolean) {
  return createInfiniteQuery(() => {
    const { q, account, agreement } = filter();
    return {
      queryKey: ['mail', 'decisions', q, account, agreement],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
        ok(
          await mailTriage(
            {
              q: q || undefined,
              account: account || undefined,
              agreement: agreement || undefined,
              limit: PAGE,
              cursor: pageParam || undefined,
            },
            { signal },
          ),
        ).data,
      initialPageParam: '',
      getNextPageParam: (last: { next_cursor?: string | null }) => last.next_cursor || undefined,
      enabled: enabled(),
      meta: { topics: TOPICS, persist: false },
    };
  });
}

export function useRules(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['mail', 'rules'],
    queryFn: async ({ signal }) => {
      const items: MailListedRule[] = [];
      let cursor: string | undefined;
      do {
        const page = ok(await mailRules({ limit: 100, cursor }, { signal })).data;
        items.push(...page.items);
        cursor = page.next_cursor || undefined;
      } while (cursor);
      return items;
    },
    enabled: enabled(),
    meta: { topics: TOPICS, persist: false },
  }));
}

export function useVocabulary(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['mail', 'vocabulary'],
    queryFn: async ({ signal }) => ok(await mailRulesVocabulary({ signal })).data,
    enabled: enabled(),
    staleTime: Number.POSITIVE_INFINITY,
    meta: { topics: TOPICS, persist: false },
  }));
}

export function useRuleDecisions(id: () => string, enabled: () => boolean) {
  return createInfiniteQuery(() => {
    const target = id();
    return {
      queryKey: ['mail', 'rule-decisions', target],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) => {
        try {
          return ok(await mailRuleDecisions(target, { limit: PAGE, cursor: pageParam || undefined }, { signal })).data;
        } catch (error) {
          if (error instanceof ApiError && error.status === 404) return null;
          throw error;
        }
      },
      initialPageParam: '',
      getNextPageParam: (last: { next_cursor?: string | null } | null) => last?.next_cursor || undefined,
      enabled: enabled() && target !== '',
      meta: { topics: TOPICS, persist: false },
    };
  });
}

export function useStatus(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['mail', 'status'],
    queryFn: async ({ signal }) => ok(await mailStatus({ signal })).data,
    enabled: enabled(),
    meta: { topics: ['mail', 'settings'], persist: false },
  }));
}

export function useChecks(enabled: () => boolean) {
  return createQuery(() => ({
    queryKey: ['mail', 'checks'],
    queryFn: async ({ signal }) => ok(await mailTriage({ limit: 5 }, { signal })).data.check,
    enabled: enabled(),
    meta: { topics: TOPICS, persist: false },
  }));
}

export function refreshRules(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['mail'] });
}

export async function handleRuleSave(id: string | undefined, rule: MailRuleInput): Promise<string> {
  if (id) return ok(await mailRuleUpdate(id, { rule })).data.id ?? id;
  return ok(await mailRuleCreate({ rule })).data.id ?? '';
}

export async function handleRuleDelete(id: string): Promise<boolean> {
  return ok(await mailRuleDelete(id)).data.deleted;
}

export async function handleRulePreview(rule: MailRuleInput): Promise<Preview> {
  return ok(await mailRulePreview({ rule })).data;
}

export type Command = 'go_live' | 'rebaseline' | 'replay';

export async function handleConfirm(action: 'go_live' | 'rebaseline'): Promise<ConfirmEffect> {
  return ok(await mailStatusConfirm({ action })).data;
}

export async function handleGoLive(token: string): Promise<void> {
  ok(await mailGoLive({ confirm_token: token }));
}

export async function handleRebaseline(token: string): Promise<void> {
  ok(await mailStatusRebaseline({ confirm_token: token }));
}

export async function handleReplay(): Promise<ReplayRun> {
  return ok(await mailReplayRun()).data;
}

export const commandWords: Record<Command, { button: string; ask: string; run: string; done: string }> = {
  go_live: { button: 'Go live', ask: 'Go live?', run: 'Go live', done: 'Mail is live.' },
  rebaseline: {
    button: 'Retake the baseline',
    ask: 'Retake the baseline?',
    run: 'Retake the baseline',
    done: 'Baseline retaken.',
  },
  replay: { button: 'Run a replay', ask: 'Run a replay now?', run: 'Run the replay', done: 'Replay finished.' },
};

export const REPLAY_EFFECT =
  'Replays the stored decisions through the current rules and counts how many would differ. It reads mail only, writes nothing to any mailbox and runs while automatic writes are paused. Only the counts are kept.';

export const unmetWords: Record<string, string> = {
  already_live: 'Mail is already live.',
  no_accounts: 'No mail account is set up and turned on.',
  unresolved_writes: 'An earlier change to the mailbox is unresolved. Record what happened to it first.',
};

export const commandRefusals: Record<string, { title: string; text: string }> = {
  not_eligible: { title: 'Go-live is not possible now', text: 'The status changed. Read the reasons below.' },
  confirm_required: { title: 'This needs a fresh confirmation', text: 'Start again and confirm the effect.' },
  confirm_stale: {
    title: 'The confirmation expired',
    text: 'It lasts ten minutes and ends when the accounts, the write cap or the mode change. Start again.',
  },
  agent_caller: {
    title: 'Only you can do this',
    text: 'Agent sign-ins cannot change the mail mode or the baseline. Sign in as the operator.',
  },
  operator_only: { title: 'Only you can do this', text: 'Tool runs cannot change the mail mode or the baseline.' },
};

export const modeWords: Record<string, string> = { live: 'Live', shadow: 'Shadow' };

export const actionWords: Record<string, string> = {
  file: 'Filing',
  flag: 'Flagging',
  trash: 'Deleting',
  correct: 'Applying your corrections',
};

export const stateWords: Record<string, string> = {
  intent: 'running',
  done: 'applied',
  applied: 'applied',
  held: 'held',
  failed: 'failed',
  partial: 'partial',
  skipped: 'skipped',
  unknown: 'outcome unknown',
  interrupted: 'interrupted',
  settled: 'checked',
  ok: 'applied',
  undone: 'undone',
};

export const stageWords: Record<string, string> = {
  correction: 'Your correction',
  operator: 'Your rule',
  learned: 'Learned from your corrections',
  knn: 'Like earlier mail you sorted',
  llm: 'The model chose it',
  fallback: 'No match, left as is',
};

export const agreementWords: Record<string, string> = {
  agree: 'Still there',
  disagree: 'Moved since',
  pending: 'Still in the inbox',
  gone: 'No longer in the mailbox',
};

export function outcomeOf(action: string, dest: string | null | undefined): string {
  if (action === 'file' && dest) return `File into ${dest}`;
  if (action === 'delete') return 'Delete';
  return 'Leave in the inbox';
}

function valueText(value: unknown): string {
  if (Array.isArray(value)) {
    const items = value.map(String);
    if (items.length < 2) return items.join('');
    return `${items.slice(0, -1).join(', ')} or ${items.at(-1)}`;
  }
  if (value === undefined || value === null) return '';
  return String(value);
}

export function conditionText(condition: MailCondition, vocabulary: RuleVocabulary | undefined): string {
  const field = vocabulary?.fields.find((item) => item.name === condition.field)?.label ?? condition.field;
  const op = vocabulary?.ops.find((item) => item.name === condition.op);
  const value = op?.value === 'none' ? '' : valueText(condition.value);
  return [field, op?.label ?? condition.op, value].filter(Boolean).join(' ');
}

export function ruleSentence(
  rule: Pick<MailListedRule, 'account' | 'action' | 'dest' | 'when'>,
  vocabulary: RuleVocabulary | undefined,
): string {
  const scope = rule.account ? `Mail to ${rule.account}` : 'Mail';
  const when = rule.when.map((item) => conditionText(item, vocabulary));
  const where = when.length > 0 ? ` where ${when.join(', and ')}` : '';
  const then =
    rule.action === 'file' && rule.dest
      ? `goes to ${rule.dest}`
      : rule.action === 'delete'
        ? 'is deleted'
        : 'stays in the inbox';
  return `${scope}${where} ${then}.`;
}

export function ruleTitle(rule: Pick<MailListedRule, 'action' | 'dest'>): string {
  return outcomeOf(rule.action, rule.dest);
}

export function rulePath(id: string): string {
  return p('/apps/mail/rules/:id', { params: { id } });
}
