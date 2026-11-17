<script lang="ts">
  import type { InferenceStatus } from '../../contract';
  import { dayOf, money } from './assistant';
  import './assistant.css';

  interface Props {
    budget: InferenceStatus;
    state: string;
  }

  let { budget, state }: Props = $props();

  const used = $derived(budget.spent_usd + budget.committed_usd + budget.reserved_usd);
</script>

<div class="asst-meter" data-state={state}>
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
    left. The budget starts again on {dayOf(budget.next_month)}.
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
