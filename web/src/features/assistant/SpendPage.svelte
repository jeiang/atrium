<script lang="ts">
  import type { SpendCap, SpendSlice } from '../../contract';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import DataTable, { type Column } from '../../shared/ui/DataTable.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import AssistantNav from './AssistantNav.svelte';
  import { classWords, dateTime, dayOf, kindWords, money, percent, useSpend, words } from './assistant';
  import SettingsGroupForm from './SettingsGroupForm.svelte';
  import './assistant.css';

  const ROUTINE_SCOPES = ['dreaming_run', 'dreaming_month', 'ops_run', 'ops_month'];
  const scopeWords: Record<string, string> = {
    dreaming_run: 'Nightly run, per run',
    dreaming_month: 'Nightly run, per month',
    ops_run: 'Server ops, per run',
    ops_month: 'Server ops, per month',
  };
  const breakdownOptions = [
    { value: 'kind', label: 'By kind' },
    { value: 'class', label: 'By budget' },
    { value: 'model', label: 'By model' },
  ];
  const columns: readonly Column[] = [
    { key: 'name', label: 'Where' },
    { key: 'cost', label: 'Cost', numeric: true },
    { key: 'calls', label: 'Calls', numeric: true },
    { key: 'share', label: 'Share', numeric: true },
  ];

  const spend = useSpend();
  const offline = $derived(unreachable(spend));
  let breakdown = $state('kind');

  const data = $derived(spend.data);
  const budget = $derived(data?.budget);
  const used = $derived(budget ? budget.spent_usd + budget.committed_usd + budget.reserved_usd : 0);
  const capOf = (scope: string): SpendCap | undefined => data?.caps.find((cap) => cap.scope === scope);
  const runCap = $derived(capOf('run'));
  const threadCap = $derived(capOf('thread'));
  const routineCaps = $derived(data?.caps.filter((cap) => ROUTINE_SCOPES.includes(cap.scope)) ?? []);
  const slices = $derived<SpendSlice[]>(
    breakdown === 'kind'
      ? (data?.by_kind ?? [])
      : breakdown === 'class'
        ? (data?.by_class ?? [])
        : (data?.by_model ?? []),
  );
  const total = $derived(slices.reduce((sum, slice) => sum + slice.cost_usd + slice.committed_usd, 0));
  const resets = $derived(data ? dayOf(data.next_month) : '');

  function label(slice: SpendSlice): string {
    if (breakdown === 'kind') return words(kindWords, slice.key);
    if (breakdown === 'class') return words(classWords, slice.key);
    return slice.key;
  }
</script>

{#snippet sliceCell(
    slice: SpendSlice,
    column: Column,
  )}
  {#if column.key === 'name'}
    {label(slice)}
  {:else if column.key === 'cost'}
    {money(slice.cost_usd + slice.committed_usd)}
  {:else if column.key === 'calls'}
    {slice.calls}
  {:else}
    {total > 0 ? percent((slice.cost_usd + slice.committed_usd) / total) : '0%'}
  {/if}
{/snippet}

<Page title="Spend" eyebrow="Assistant" lede="What the assistant cost this month, where it went, and where it stops.">
  <AssistantNav current="spend" />
  <div class="asst">
    {#if offline}
      <Section id="month" title="This month">
        <ViewState kind="offline" title="Cannot reach atrium" level={3}
          >Spending loads when atrium answers again.</ViewState
        >
      </Section>
    {:else if spend.isError && !data}
      <Section id="month" title="This month">
        <ViewState kind="error" title="Spending did not load" level={3}>
          {spend.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void spend.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      </Section>
    {:else if !data || !budget}
      <Section id="month" title="This month">
        <Skeleton lines={4} label="Loading spending" />
      </Section>
    {:else}
      <Section id="month" title="This month" meta={data.month}>
        <div class="asst-meter" data-state={data.state}>
          <meter
            class="asst-meter__bar"
            min="0"
            max={budget.budget_usd > 0 ? budget.budget_usd : 1}
            value={used}
            low={budget.unattended_ceiling_usd}
            high={budget.interactive_ceiling_usd}
            optimum="0"
            aria-label="Budget used"
          ></meter>
          <p class="asst-meter__text">
            <strong>{money(budget.spent_usd)}</strong>
            spent of {money(budget.budget_usd)}.
            {#if budget.committed_usd + budget.reserved_usd > 0}
              {money(budget.committed_usd + budget.reserved_usd)}
              is held for runs in progress.
            {/if}
            {money(budget.remaining_usd)}
            left. The budget starts again on {resets}.
          </p>
          {#if budget.carry_in_usd > 0 || budget.unknown_usd > 0}
            <p class="asst-meta">
              {#if budget.carry_in_usd > 0}
                {money(budget.carry_in_usd)}
                carried in from last month.
              {/if}
              {#if budget.unknown_usd > 0}
                {money(budget.unknown_usd)}
                has no confirmed price yet.
              {/if}
            </p>
          {/if}
        </div>
        {#if data.state === 'exhausted'}
          <Notice tone="error" variant="inline" title="The budget is used up">
            atrium refuses new chat messages and scheduled runs until {resets}. Raise the monthly budget below to go on.
          </Notice>
        {:else if data.state === 'near'}
          <Notice tone="warn" variant="inline" title="Scheduled work has stopped">
            Scheduled and background runs reached {percent(budget.share)} of the budget and are refused until {resets}.
            Chat still works until {money(budget.interactive_ceiling_usd)}.
          </Notice>
        {/if}
        <ul class="asst-facts" aria-label="Where atrium stops">
          {#each data.headroom as room (room.class)}
            <li data-class={room.class}>
              {words(classWords, room.class)}:
              {#if room.refused}
                refused, the {money(room.ceiling_usd)} ceiling is reached.
              {:else}
                {money(room.headroom_usd)}
                left before the {money(room.ceiling_usd)} ceiling.
              {/if}
            </li>
          {/each}
          {#if runCap}
            <li data-cap="run">
              A run that would pass {money(runCap.value)} is stopped before the request that would cross it, and says so
              in the thread.
            </li>
          {/if}
          {#if threadCap}
            <li data-cap="thread">
              A thread, with its research helpers, stops the same way at {money(threadCap.value)}.
            </li>
          {/if}
        </ul>
      </Section>

      <Section id="breakdown" title="Where it went">
        <Segmented label="Breakdown" options={breakdownOptions} bind:value={breakdown} />
        <DataTable
          caption="Spending {breakdownOptions.find((option) => option.value === breakdown)?.label.toLowerCase()}"
          {columns}
          rows={slices}
          key={(slice) => slice.key}
          cell={sliceCell}
          empty="No paid run has happened this month."
        />
      </Section>

      <Section id="runs" title="Largest runs">
        {#if data.top_runs.length === 0}
          <ViewState kind="empty" title="No runs yet" level={3}
            >The most expensive runs of the month show here.</ViewState
          >
        {:else}
          <List label="Largest runs">
            {#each data.top_runs as run (run.run_id)}
              <ListRow
                title={run.title || `${words(kindWords, run.kind)} run ${run.run_id}`}
                meta="{words(kindWords, run.kind)}, {run.model}, {dateTime(run.started_at)}, {money(run.cost_usd + run.committed_usd)}"
                href={run.thread_id ? p('/chat/:id', { params: { id: String(run.thread_id) } }) : undefined}
              />
            {/each}
          </List>
        {/if}
      </Section>
    {/if}

    <SettingsGroupForm
      id="caps"
      title="Budget and caps"
      groups={['assistant.spend']}
      saveLabel="Save spending limits"
      savedText="atrium checks the next request against the new limits."
      empty="atrium offers no spending settings right now."
    />

    {#if routineCaps.length > 0}
      <Section id="routine-caps" title="Routine caps">
        <ul class="asst-facts" aria-label="Routine caps">
          {#each routineCaps as cap (cap.key)}
            <li data-cap={cap.scope}>{scopeWords[cap.scope] ?? cap.scope}: {money(cap.value)}</li>
          {/each}
        </ul>
        <div class="cluster">
          <a class="asst-link" href={p('/assistant/routines')}>Change them in Routines</a>
        </div>
      </Section>
    {/if}
  </div>
</Page>
