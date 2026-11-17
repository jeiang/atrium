<script lang="ts">
  import { tick } from 'svelte';
  import { ApiError } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, refusedOffline, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import MailFrame from './MailFrame.svelte';
  import { type Problem, problemOf } from './mail';
  import { handleRuleMove, mailEnabled, ruleTitle, useRules, useVocabulary } from './manage';
  import RuleCard from './RuleCard.svelte';

  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  const rules = useRules(() => ready);
  const vocabulary = useVocabulary(() => ready);

  let moving = $state<{ id: string; direction: 'up' | 'down' }>();
  let problem = $state<Problem>();
  let unsure = $state(false);

  const blocked = $derived(!device.online || unsure || moving !== undefined);

  async function onMove(id: string, direction: 'up' | 'down'): Promise<void> {
    const rule = rules.data?.find((item) => item.id === id);
    if (moving || unsure || !device.online || !rule) return;
    const title = ruleTitle(rule);
    moving = { id, direction };
    problem = undefined;
    try {
      const moved = await handleRuleMove(id, direction);
      await rules.refetch();
      const at = (rules.data?.findIndex((item) => item.id === id) ?? -1) + 1;
      toast(moved && at > 0 ? `${title} is now rule ${at}.` : `${title} did not move.`);
    } catch (error) {
      problem = problemOf(error);
      unsure = !(error instanceof ApiError) && !refusedOffline(error);
    } finally {
      moving = undefined;
    }
    await tick();
    const own = document.querySelector<HTMLButtonElement>(`[data-move="${CSS.escape(`${id}:${direction}`)}"]`);
    const other = document.querySelector<HTMLButtonElement>(
      `[data-move="${CSS.escape(`${id}:${direction === 'up' ? 'down' : 'up'}`)}"]`,
    );
    (own?.disabled && other ? other : own)?.focus();
  }

  async function onCheck(): Promise<void> {
    const result = await rules.refetch();
    if (result.isError) return;
    unsure = false;
    problem = undefined;
  }
</script>

<MailFrame title="Rules" current={p('/apps/mail/rules')}>
  {#snippet actions()}
    {#if device.online && ready}
      <a class="btn btn--primary" href={p('/apps/mail/rules/:id', { params: { id: 'new' } })}>New rule</a>
    {/if}
  {/snippet}
  <p class="mail-lede">
    Rules run first, from the top. The first rule that matches a message wins, so a rule higher up beats one below it. A
    message that matches no rule is sorted the usual way.
  </p>
  {#if unreachable(rules) || (!device.online && !rules.data)}
    <ViewState kind="offline" title="Cannot reach atrium">
      Rules load when atrium answers again. Nothing from the mailbox is kept on this device.
    </ViewState>
  {:else if rules.isError && !rules.data}
    <ViewState kind="error" title="Rules did not load">
      {rules.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void rules.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !rules.data}
    <ul class="mail-list" aria-label="Loading rules">
      {#each [0, 1, 2] as key (key)}
        <li class="mail-list__skeleton"><Skeleton lines={2} label="Loading rules" /></li>
      {/each}
    </ul>
  {:else if rules.data.length === 0}
    <ViewState kind="empty" title="No rules yet">
      Write a rule to always sort some mail the same way. Correcting a decision can also make one.
    </ViewState>
  {:else}
    <p class="meta">{plural(rules.data.length, 'rule')}</p>
    {#if problem}
      <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
      {#if unsure}
        <Button variant="secondary" disabled={!device.online} onclick={() => void onCheck()}>Check the order</Button>
      {/if}
    {/if}
    <ol class="mail-list" aria-label="Rules">
      {#each rules.data as rule, index (rule.id)}
        <RuleCard
          {rule}
          vocabulary={vocabulary.data}
          position={index + 1}
          total={rules.data.length}
          moving={moving?.id === rule.id ? moving.direction : undefined}
          {blocked}
          onmove={(direction) => void onMove(rule.id, direction)}
        />
      {/each}
    </ol>
  {/if}
</MailFrame>
