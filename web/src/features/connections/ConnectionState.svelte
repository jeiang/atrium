<script lang="ts">
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import { words } from './connections';

  let { state, graceUntil }: { state: string; graceUntil?: string } = $props();
  const shown = $derived(words(state, graceUntil));
</script>

<span class="conn-state" data-state={state} data-grace={graceUntil ? 'true' : undefined}>
  <StatusMark tone={shown.tone}>{shown.label}</StatusMark>
</span>
