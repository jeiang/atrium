<script lang="ts">
  import { useShell } from './frame';
  import NotFound from './NotFound.svelte';
  import { appById, moduleOn } from './nav';
  import Placeholder from './Placeholder.svelte';
  import { route } from './router';

  const shell = useShell();
  const app = $derived(route.params.app ? appById(route.params.app) : undefined);
</script>

{#if app && moduleOn(shell.data, app.module)}
  <Placeholder title={app.title} legacy={app.legacy} />
{:else if !shell.isPending}
  <NotFound />
{/if}
