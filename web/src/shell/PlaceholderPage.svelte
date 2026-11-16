<script lang="ts">
  import NotFound from './NotFound.svelte';
  import { pages, plan } from './nav';
  import Placeholder from './Placeholder.svelte';
  import { isActive } from './router';

  const destination = $derived(
    isActive.startsWith('/tasks') ? plan : pages.find((item) => item.path !== '/' && isActive.startsWith(item.path)),
  );
</script>

{#if destination}
  <Placeholder title={destination.label} legacy={destination.legacy} />
{:else}
  <NotFound />
{/if}
