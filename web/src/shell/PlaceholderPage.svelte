<script lang="ts">
  import NotFound from './NotFound.svelte';
  import { pages } from './nav';
  import Placeholder from './Placeholder.svelte';
  import { isActive } from './router';

  const destination = $derived(pages.find((item) => item.path !== '/' && isActive.startsWith(item.path)));
</script>

{#if destination}
  <Placeholder title={destination.label} legacy={destination.legacy} />
{:else}
  <NotFound />
{/if}
