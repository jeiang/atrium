<script lang="ts">
  import { device } from '../../shared/device.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { isActive, p } from '../../shell/router';
  import Catalogue from './Catalogue.svelte';
  import Following from './Following.svelte';
  import Intake from './Intake.svelte';
  import LibraryNav from './LibraryNav.svelte';
  import { blockedBy, type View } from './library';
  import './library.css';

  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'media'));
  const blocked = $derived(blockedBy(app));
  const ready = $derived(app?.enabled === true && blocked === undefined);
  const view = $derived<View>(
    isActive('/apps/media/following') ? 'following' : isActive('/apps/media/intake') ? 'intake' : 'catalogue',
  );
  const titles: Record<View, string> = { catalogue: 'Library', following: 'Following', intake: 'Intake' };
  const ledes: Record<View, string> = {
    catalogue: 'Movies and shows on the library server.',
    following: 'Shows atrium watches for new episodes.',
    intake: 'Finished downloads waiting to be filed.',
  };
</script>

{#if appGone(apps, 'media')}
  <NotFound />
{:else}
  <Page title={titles[view]} eyebrow={view === 'catalogue' ? undefined : 'Library'} lede={ledes[view]}>
    <a class="link lib-back" href={p('/apps')}>Apps</a>
    <LibraryNav current={view} />
    {#if blocked}
      <ViewState kind="blocked" title="Library is unavailable">
        {blocked}
        Turn it on in the apps directory, then Library opens here.
        {#snippet action()}
          <a class="link lib-link" href={p('/apps/directory')}>Open the apps directory</a>
        {/snippet}
      </ViewState>
    {:else if !apps.data && !device.online}
      <ViewState kind="offline" title="Cannot reach atrium">Library loads when atrium answers again.</ViewState>
    {:else}
      {#if app?.state === 'needs-settings' && app.reason}
        <Notice tone="warn" title="Library needs settings">
          {app.reason}
          Some of Library may not load until this is repaired in the apps hub.
        </Notice>
      {/if}
      {#if view === 'following'}
        <Following {ready} />
      {:else if view === 'intake'}
        <Intake {ready} />
      {:else}
        <Catalogue {ready} />
      {/if}
    {/if}
  </Page>
{/if}
