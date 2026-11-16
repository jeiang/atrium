<script lang="ts">
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useShell } from '../../shell/frame';
  import { openApps, useApps } from '../../shell/manifest';
  import { appsDestination, assistant, knowledge, moduleOn, plan, settings } from '../../shell/nav';
  import { p } from '../../shell/router';
  import Tiles from '../../shell/Tiles.svelte';
  import AppCard from './AppCard.svelte';
  import './apps.css';

  const shell = useShell();
  const apps = useApps();
  const offline = $derived(unreachable(apps));
  const open = $derived(openApps(apps.data));
  const places = $derived(
    [plan, knowledge, assistant, settings]
      .filter((item) => moduleOn(shell.data, item.module))
      .map((item) => ({ key: item.path, href: p(item.path), label: item.label, icon: item.icon })),
  );
</script>

<Page title={appsDestination.label}>
  {#snippet actions()}
    <a class="btn btn--secondary" href={p('/apps/directory')}>All apps</a>
  {/snippet}
  <div class="apps-places">
    <Tiles tiles={places} label="Places" />
  </div>
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">The apps load when atrium answers again.</ViewState>
  {:else if apps.isError && !apps.data}
    <ViewState kind="error" title="The apps did not load">
      {apps.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void apps.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !apps.data}
    <Skeleton lines={6} label="Loading the apps" />
  {:else if open.length === 0}
    <ViewState kind="empty" title="No apps are on">
      Turn one on in the directory.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/apps/directory')}>Open the directory</a>
      {/snippet}
    </ViewState>
  {:else}
    <ul class="app-grid" aria-label="Apps">
      {#each open as app (app.id)}
        <li data-app={app.id}><AppCard {app} links level={2} /></li>
      {/each}
    </ul>
  {/if}
</Page>
