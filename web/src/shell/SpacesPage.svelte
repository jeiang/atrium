<script lang="ts">
  import Page from '../shared/ui/Page.svelte';
  import { useShell } from './frame';
  import { appsDestination, assistant, knowledge, mailStatus, moduleOn, plan, settings, shownApps } from './nav';
  import { p } from './router';
  import Tiles from './Tiles.svelte';

  const shell = useShell();
  const places = $derived(
    [plan, knowledge, appsDestination, assistant, settings]
      .filter((item) => moduleOn(shell.data, item.module))
      .map((item) => ({ key: item.path, href: p(item.path), label: item.label, icon: item.icon })),
  );
  const appTiles = $derived(
    shownApps(shell.data).map((app) => ({
      key: app.id,
      href: p('/apps/:app', { params: { app: app.id } }),
      label: app.title,
      icon: app.icon,
      meta: app.id === 'mail' ? mailStatus(shell.data) : undefined,
    })),
  );
</script>

<Page title="Spaces">
  <Tiles tiles={places} label="Places" />
  {#if appTiles.length > 0}
    <h2 class="tiles-label">Apps</h2>
    <Tiles tiles={appTiles} label="Apps" />
  {/if}
</Page>
