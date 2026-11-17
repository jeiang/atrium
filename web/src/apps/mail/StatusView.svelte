<script lang="ts">
  import { formatDayTime, plural } from '../../shared/assistant/format';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { p } from '../../shell/router';
  import DecisionCard from './DecisionCard.svelte';
  import {
    actionWords,
    type Command,
    commandWords,
    type Decision,
    type MailStatus,
    modeWords,
    type ReplayRun,
    stateWords,
    unmetWords,
  } from './manage';

  interface Props {
    status: MailStatus;
    checks: Decision[] | undefined;
    replay?: ReplayRun;
    blocked?: Command;
    checking?: boolean;
    onask: (command: Command) => void;
    oncheck: () => void;
  }

  let { status: s, checks, replay, blocked, checking = false, onask, oncheck }: Props = $props();

  const uid = $props.id();
  const live = $derived(s.mode === 'live');
  const reasons = $derived(s.unmet.filter((item) => item !== 'already_live'));
  const shown = $derived(replay ?? s.replay ?? undefined);
  const actions = $derived(Object.entries(s.shadow.actions));
  const watch = $derived(s.watch_closely);
  const offReason = $derived(!device.online ? 'Cannot reach atrium.' : blocked ? 'Check the status first.' : undefined);

  function count(states: Record<string, number>): string {
    return Object.entries(states)
      .map(([state, n]) => `${n} ${stateWords[state] ?? state}`)
      .join(', ');
  }
</script>

{#if blocked}
  <Notice tone="error" title="The outcome is unknown">
    atrium did not hear back after "{commandWords[blocked].run}" and did not try again. Check the status to see whether
    it happened; the commands stay off until then.
    {#snippet action()}
      <Button variant="secondary" pending={checking} disabled={!device.online} onclick={oncheck}
        >Check the status</Button
      >
    {/snippet}
  </Notice>
{/if}
<section class="mail-section" aria-labelledby="{uid}-now">
  <h2 id="{uid}-now" class="mail-section__title">Mode</h2>
  <p class="mail-mode">
    Mail is in <strong>{modeWords[s.mode] ?? s.mode}</strong> mode{s.paused ? ', and automatic writes are paused' : ''}.
  </p>
  <p class="meta">
    {live
    ? 'atrium files, flags and deletes mail in the mailboxes automatically, within the write cap.'
    : 'atrium records what it would do and changes nothing in the mailboxes.'}
    Pause is part of the assistant's authority.
    <a class="link" href={p('/assistant/authority')}>Open the pause</a>
  </p>
  {#if watch.active}
    <Notice tone="info" title={watch.until ? `Watch closely until ${formatDayTime(watch.until)}` : 'Watch closely'}>
      The mode changed to
      {modeWords[watch.mode ?? ''] ?? watch.mode ?? 'a new mode'}{watch.since ? ` on ${formatDayTime(watch.since)}` : ''}.
      For seven days, check the decisions that need a look.
    </Notice>
    <section class="mail-section" aria-labelledby="{uid}-watch">
      <h3 id="{uid}-watch" class="mail-section__title">Decisions to check</h3>
      {#if checks && checks.length > 0}
        <ul class="mail-list mail-list--cards" aria-label="Decisions to check">
          {#each checks as decision (decision.decision_id)}
            <DecisionCard {decision} />
          {/each}
        </ul>
      {:else if checks}
        <p class="meta">Nothing needs a look right now.</p>
      {:else}
        <Skeleton lines={2} label="Loading decisions to check" />
      {/if}
      <a class="link mail-up" href={p('/apps/mail/decisions')}>All decisions</a>
    </section>
  {/if}
  {#if !live}
    {#if s.eligible}
      <span class="cluster">
        <Button variant="primary" disabled={!!offReason} reason={offReason} onclick={() => onask('go_live')}
          >{commandWords.go_live.button}</Button
        >
      </span>
    {:else}
      <Notice tone="warn" variant="inline" title="Go-live is not possible now">
        <ul class="mail-reasons" aria-label="Why not">
          {#each reasons as reason (reason)}
            <li>{unmetWords[reason] ?? reason}</li>
          {/each}
        </ul>
      </Notice>
    {/if}
  {/if}
</section>
<section class="mail-section" aria-labelledby="{uid}-baseline">
  <h2 id="{uid}-baseline" class="mail-section__title">Baseline</h2>
  <p>
    {plural(s.baseline.messages, 'message')}{s.baseline.taken_at ? `, taken ${formatDayTime(s.baseline.taken_at)}` : '; not taken yet'}.
  </p>
  <p class="meta">Mail in the baseline is never touched by the automatic inbox pass.</p>
  <span class="cluster">
    <Button variant="secondary" disabled={!!offReason} reason={offReason} onclick={() => onask('rebaseline')}
      >{commandWords.rebaseline.button}</Button
    >
  </span>
</section>
<section class="mail-section" aria-labelledby="{uid}-actions">
  <h2 id="{uid}-actions" class="mail-section__title">Automatic actions</h2>
  {#if actions.length === 0}
    <p class="meta">No automatic actions recorded.</p>
  {:else}
    <dl class="mail-facts">
      {#each actions as [kind, states] (kind)}
        <dt>{actionWords[kind] ?? kind}</dt>
        <dd>{count(states)}</dd>
      {/each}
    </dl>
  {/if}
</section>
<section class="mail-section" aria-labelledby="{uid}-replay">
  <h2 id="{uid}-replay" class="mail-section__title">Replay</h2>
  {#if shown}
    <p data-replay>
      Last run {formatDayTime(shown.at)}: {plural(shown.messages, 'message')} replayed, {shown.differ} would now be
      sorted differently.
    </p>
  {:else}
    <p class="meta">No replay has run yet.</p>
  {/if}
  <span class="cluster">
    <Button variant="secondary" disabled={!!offReason} reason={offReason} onclick={() => onask('replay')}
      >{commandWords.replay.button}</Button
    >
  </span>
</section>
<section class="mail-section" aria-labelledby="{uid}-deletions">
  <h2 id="{uid}-deletions" class="mail-section__title">Deletions</h2>
  <p>
    {s.deletions.pending_proposals === 0
    ? 'No deletion rule proposals wait for review.'
    : `${plural(s.deletions.pending_proposals, 'deletion rule proposal')} wait for review.`}
    {#if s.deletions.pending_proposals > 0}
      <a class="link" href={p('/inbox')}>Review in Inbox</a>
    {/if}
  </p>
</section>
<section class="mail-section" aria-labelledby="{uid}-accounts">
  <h2 id="{uid}-accounts" class="mail-section__title">Accounts covered</h2>
  <p>{s.accounts.length === 0 ? 'None.' : s.accounts.join(', ')}</p>
  <a class="link mail-up" href={p('/apps/mail/accounts')}>Mail accounts</a>
</section>
