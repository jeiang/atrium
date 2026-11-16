<script lang="ts">
  import { useShell } from './frame';
  import { appsDestination, mailStatus, shownApps } from './nav';
  import Placeholder from './Placeholder.svelte';
  import { p } from './router';
  import Tiles from './Tiles.svelte';

  const shell = useShell();
  const tiles = $derived(
    shownApps(shell.data).map((app) => ({
      key: app.id,
      href: p('/apps/:app', { params: { app: app.id } }),
      label: app.title,
      icon: app.icon,
      meta: app.id === 'mail' ? mailStatus(shell.data) : undefined,
    })),
  );
</script>

<Placeholder title={appsDestination.label} legacy={appsDestination.legacy}>
  {#if tiles.length > 0}
    <Tiles {tiles} label="Apps" />
  {/if}
</Placeholder>
