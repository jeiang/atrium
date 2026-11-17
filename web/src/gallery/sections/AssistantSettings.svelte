<script lang="ts">
  import type { InferenceStatus } from '../../contract';
  import AssistantNav from '../../features/assistant/AssistantNav.svelte';
  import { runStatusWords } from '../../features/assistant/assistant';
  import SpendMeter from '../../features/assistant/SpendMeter.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Section from '../../shared/ui/Section.svelte';

  const next = Math.floor(new Date(2026, 11, 1).getTime() / 1000);

  function budget(spent: number, held: number): InferenceStatus {
    return {
      budget_usd: 150,
      spent_usd: spent,
      committed_usd: held,
      reserved_usd: 0,
      unknown_usd: spent > 140 ? 0.42 : 0,
      expired_usd: 0,
      carry_in_usd: 0,
      remaining_usd: Math.max(0, 150 - spent - held),
      share: 0.8,
      interactive_ceiling_usd: 150,
      unattended_ceiling_usd: 120,
      next_month: next,
    };
  }

  const meters = [
    { state: 'ok', budget: budget(42.18, 0.35) },
    { state: 'near', budget: budget(124.5, 0) },
    { state: 'exhausted', budget: budget(150, 0) },
  ];
</script>

<Section id="g-assistant-settings" data-gallery="assistant-settings" title="Assistant destination">
  <AssistantNav current="routines" />
  <div class="grid">
    {#each meters as meter (meter.state)}
      <SpendMeter budget={meter.budget} state={meter.state} />
    {/each}
  </div>
  <div class="cluster">
    {#each Object.entries(runStatusWords) as [status, words] (status)}
      <Badge tone={words.tone}>{words.label}</Badge>
    {/each}
  </div>
</Section>
