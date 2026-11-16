<script lang="ts">
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p, route } from '../../shell/router';
  import AppCard from './AppCard.svelte';
  import './apps.css';

  const apps = useApps();
  const offline = $derived(unreachable(apps));
  const app = $derived(apps.data?.find((item) => item.id === route.params.app && item.enabled));
</script>

{#if app}
  <Page title={app.name} eyebrow="Apps">
    <a class="link apps-back" href={p('/apps')}>Apps</a>
    <AppCard {app} titled={false} links />
  </Page>
{:else if offline}
  <Page title="Apps">
    <ViewState kind="offline" title="Cannot reach atrium">This app loads when atrium answers again.</ViewState>
  </Page>
{:else if apps.isError && !apps.data}
  <Page title="Apps">
    <ViewState kind="error" title="The app did not load">
      {apps.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void apps.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  </Page>
{:else if apps.data}
  <NotFound />
{/if}
