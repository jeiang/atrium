<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import MailFrame from './MailFrame.svelte';
  import { mailEnabled, useRules, useVocabulary } from './manage';
  import RuleCard from './RuleCard.svelte';

  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  const rules = useRules(() => ready);
  const vocabulary = useVocabulary(() => ready);
</script>

<MailFrame title="Rules" current={p('/apps/mail/rules')}>
  {#snippet actions()}
    {#if device.online && ready}
      <a class="btn btn--primary" href={p('/apps/mail/rules/:id', { params: { id: 'new' } })}>New rule</a>
    {/if}
  {/snippet}
  <p class="mail-lede">Rules run first, in this order. A message that matches no rule is sorted the usual way.</p>
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
    <ol class="mail-list" aria-label="Rules">
      {#each rules.data as rule (rule.id)}
        <RuleCard {rule} vocabulary={vocabulary.data} />
      {/each}
    </ol>
  {/if}
</MailFrame>
