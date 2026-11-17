<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError } from '../../contract';
  import { formatClock } from '../../shared/assistant/format';
  import { device, refusedOffline } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { p } from '../../shell/router';
  import { type Problem, problemOf, unknownOutcome } from './mail';
  import {
    type Command,
    type ConfirmEffect,
    commandRefusals,
    commandWords,
    handleConfirm,
    handleGoLive,
    handleRebaseline,
    handleReplay,
    REPLAY_EFFECT,
    type ReplayRun,
  } from './manage';

  interface Props {
    command: Command;
    open: boolean;
    ondone: (command: Command, run?: ReplayRun) => void;
    onunknown: (command: Command) => void;
  }

  let { command, open = $bindable(), ondone, onunknown }: Props = $props();

  const words = $derived(commandWords[command]);
  let offer = $state<ConfirmEffect>();
  let loading = $state(false);
  let running = $state(false);
  let problem = $state<Problem>();
  let stale = $state(false);
  let asked = 0;

  const needsToken = $derived(command !== 'replay');
  const canRun = $derived(device.online && !loading && !running && (!needsToken || offer !== undefined));

  $effect(() => {
    if (open) untrack(() => void start());
  });

  function problemFor(error: unknown): Problem {
    if (error instanceof ApiError) {
      const known = commandRefusals[error.code];
      if (known) return { tone: 'warn', title: known.title, text: `${known.text} Nothing was changed.` };
    }
    return problemOf(error);
  }

  async function start(): Promise<void> {
    const ask = ++asked;
    offer = undefined;
    problem = undefined;
    stale = false;
    if (!needsToken || loading) return;
    loading = true;
    try {
      const got = await handleConfirm(command === 'go_live' ? 'go_live' : 'rebaseline');
      if (ask === asked && open) offer = got;
    } catch (error) {
      if (ask === asked) problem = problemFor(error);
    } finally {
      loading = false;
    }
  }

  async function onRun(): Promise<void> {
    if (!canRun) return;
    running = true;
    problem = undefined;
    try {
      let run: ReplayRun | undefined;
      if (command === 'replay') run = await handleReplay();
      else if (command === 'go_live') await handleGoLive(offer?.confirm_token ?? '');
      else await handleRebaseline(offer?.confirm_token ?? '');
      open = false;
      ondone(command, run);
    } catch (error) {
      if ((error instanceof ApiError && !unknownOutcome(error)) || refusedOffline(error)) {
        problem = problemFor(error);
        stale = error instanceof ApiError && (error.code === 'confirm_stale' || error.code === 'confirm_required');
        offer = undefined;
      } else {
        open = false;
        onunknown(command);
      }
    } finally {
      running = false;
    }
  }
</script>

<Dialog bind:open title={words.ask} pending={running} initialFocus="cancel">
  {#if command === 'replay'}
    <p>{REPLAY_EFFECT}</p>
    {#if running}
      <p role="status">The replay is running. This can take a few minutes; keep this open.</p>
    {/if}
  {:else if loading}
    <Skeleton lines={3} label="Asking atrium what this would do" />
  {:else if offer}
    <p class="mail-statement">{offer.statement}</p>
    <dl class="mail-facts">
      <dt>Accounts</dt>
      <dd>{offer.accounts.join(', ') || 'None'}</dd>
      <dt>Write cap</dt>
      <dd>{offer.write_cap} per account per pass</dd>
      <dt>Baseline</dt>
      <dd>About {offer.baseline} messages</dd>
      <dt>Confirm by</dt>
      <dd><time datetime={new Date(offer.expires_at * 1000).toISOString()}>{formatClock(offer.expires_at)}</time></dd>
    </dl>
    <p>{offer.pause} <a class="link" href={p('/assistant/authority')}>Open the pause</a></p>
  {/if}
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={running} onclick={() => (open = false)}>Cancel</Button>
    {#if stale || (needsToken && !offer && !loading)}
      <Button
        variant="secondary"
        disabled={!device.online}
        reason={device.online ? undefined : 'Cannot reach atrium.'}
        onclick={() => void start()}
        >Start again</Button
      >
    {:else}
      <Button
        variant={command === 'replay' ? 'primary' : 'danger'}
        pending={running}
        disabled={!canRun}
        reason={device.online ? undefined : 'Cannot reach atrium.'}
        onclick={() => void onRun()}
        >{words.run}</Button
      >
    {/if}
  {/snippet}
</Dialog>
