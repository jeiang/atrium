<script lang="ts">
  import type { InboxId } from '../../shared/assistant/inboxId';
  import AskCard from './AskCard.svelte';
  import InboxCard from './InboxCard.svelte';

  let { target, ondecided }: { target: InboxId; ondecided?: (key: string) => void } = $props();
</script>

{#if target.type === 'proposal' || target.type === 'draft'}
  <InboxCard {target} {ondecided} />
{:else}
  <AskCard {target} {ondecided} />
{/if}
