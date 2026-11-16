<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { type AppEntry, appsSwitch, ok } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { refreshApps, useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import AppCard from './AppCard.svelte';
  import { gesture, type Outcome, outcome } from './apps';
  import './apps.css';

  const client = useQueryClient();
  const apps = useApps();
  const offline = $derived(unreachable(apps));

  let pending = $state<Record<string, boolean | undefined>>({});
  let outcomes = $state<Record<string, Outcome | undefined>>({});

  async function onSwitch(app: AppEntry, next: boolean): Promise<void> {
    if (pending[app.id] !== undefined) return;
    pending = { ...pending, [app.id]: next };
    outcomes = { ...outcomes, [app.id]: undefined };
    try {
      const entry = ok(await appsSwitch(app.id, { enabled: next }, gesture(app.revision))).data;
      client.setQueryData<AppEntry[]>(['apps'], (rows) => rows?.map((row) => (row.id === entry.id ? entry : row)));
      announce(`${entry.name} is ${entry.enabled ? 'on' : 'off'}`);
    } catch (error) {
      const result = outcome(error, app);
      outcomes = { ...outcomes, [app.id]: result };
      announce(`${result.title}. ${result.text}`);
    } finally {
      pending = { ...pending, [app.id]: undefined };
      refreshApps(client);
    }
  }
</script>

<Page title="All apps" eyebrow="Apps">
  <a class="link apps-back" href={p('/apps')}>Apps</a>
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
    <Skeleton lines={8} label="Loading the apps" />
  {:else if apps.data.length === 0}
    <ViewState kind="empty" title="No apps">This atrium has no apps to switch.</ViewState>
  {:else}
    <ul class="app-grid" aria-label="All apps">
      {#each apps.data as app (app.id)}
        <li id="app-{app.id}" data-app={app.id} tabindex="-1">
          <AppCard
            {app}
            pending={pending[app.id] !== undefined}
            target={pending[app.id]}
            outcome={outcomes[app.id]}
            onswitch={(next) => void onSwitch(app, next)}
          />
        </li>
      {/each}
    </ul>
  {/if}
</Page>
