<script lang="ts">
  import { type Stuck, stuckChoices } from '../../shared/assistant/stuck';
  import type { Outcome } from '../../shared/assistant/types';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import { type Problem, problemOf } from './mail';

  interface Props {
    stuck: Stuck;
    title: string;
    subject: string;
    onresolve: (outcome: string) => Promise<void>;
    ondone: (said: string) => void;
  }

  let { stuck, title, subject, onresolve, ondone }: Props = $props();

  const choices = $derived(stuckChoices[stuck]);
  let asking = $state(false);
  let resolving = $state(false);
  let chosen = $state<Outcome>();
  let problem = $state<Problem>();

  function onAsk(choice: Outcome): void {
    chosen = choice;
    problem = undefined;
    asking = true;
  }

  async function onResolve(): Promise<void> {
    const choice = chosen;
    if (!choice || resolving) return;
    resolving = true;
    try {
      await onresolve(choice.value);
      asking = false;
      ondone(choice.done);
    } catch (error) {
      problem = problemOf(error);
    } finally {
      resolving = false;
    }
  }
</script>

<Notice tone="warn" {title}>
  {choices.reason}
  {#snippet action()}
    <span class="cluster">
      {#each choices.outcomes as choice (choice.value)}
        <Button
          variant="secondary"
          disabled={!device.online}
          reason={device.online ? undefined : 'Cannot reach atrium.'}
          onclick={() => onAsk(choice)}
          >{choice.label}</Button
        >
      {/each}
    </span>
  {/snippet}
</Notice>

<Dialog bind:open={asking} title={chosen?.ask ?? ''} pending={resolving} initialFocus="cancel">
  <p>{subject}. {chosen?.effect}</p>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={resolving} onclick={() => (asking = false)}>Cancel</Button>
    <Button variant="primary" pending={resolving} disabled={!device.online} onclick={() => void onResolve()}
      >{chosen?.label}</Button
    >
  {/snippet}
</Dialog>
