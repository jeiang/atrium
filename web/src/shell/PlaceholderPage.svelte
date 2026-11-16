<script lang="ts">
  import NotFound from './NotFound.svelte';
  import { pages } from './nav';
  import Placeholder from './Placeholder.svelte';
  import { isActive, p } from './router';

  const destination = $derived(pages.find((item) => item.path !== '/' && isActive.startsWith(item.path)));
</script>

{#if destination?.path === '/settings'}
  <Placeholder title={destination.label} legacy={destination.legacy}>
    <nav class="cluster" aria-label="Settings pages">
      <a class="btn btn--secondary" href={p('/settings/connections')}>Connections</a>
    </nav>
  </Placeholder>
{:else if destination}
  <Placeholder title={destination.label} legacy={destination.legacy} />
{:else}
  <NotFound />
{/if}
