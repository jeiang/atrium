<script lang="ts">
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navLinks, repairHref, unmetNeeds, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p, route } from '../../shell/router';
  import AppCard from './AppCard.svelte';
  import './apps.css';

  const apps = useApps();
  const offline = $derived(unreachable(apps));
  const app = $derived(apps.data?.find((item) => item.id === route.params.app && item.enabled));
  const blank = $derived(
    app !== undefined && app.state === 'enabled' && unmetNeeds(app).length === 0 && navLinks(app).length === 0,
  );
  const linked = $derived(app?.needs.find((need) => need.connection));
</script>

{#if app}
  <Page title={app.name} eyebrow="Apps">
    <a class="link apps-back" href={p('/apps')}>Apps</a>
    {#if blank}
      <ViewState kind="empty" title="Nothing to show here" level={2}>
        {app.name}
        works through the assistant. Ask for it in a chat.
        {#snippet action()}
          {#if linked}
            <a class="btn btn--secondary" href={repairHref(linked)}>Connection settings</a>
          {:else}
            <a class="btn btn--secondary" href="{p('/apps/directory')}#app-{app.id}">App settings</a>
          {/if}
        {/snippet}
      </ViewState>
    {:else}
      <AppCard {app} titled={false} links />
    {/if}
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
