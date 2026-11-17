<script lang="ts">
  import type { Snippet } from 'svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, repairHref, stateOf, unmetNeeds, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import MailNav from './MailNav.svelte';
  import './mail.css';

  interface Props {
    title: string;
    current: string;
    actions?: Snippet;
    children: Snippet;
    after?: Snippet;
  }

  let { title, current, actions, children, after }: Props = $props();

  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'mail'));
  const needs = $derived(app ? unmetNeeds(app) : []);
</script>

{#if appGone(apps, 'mail')}
  <NotFound />
{:else}
  <Page {title} eyebrow="Mail" {actions}>
    <a class="link mail-back" href={p('/apps')}>Apps</a>
    <MailNav {app} {current} />
    {#if app && !app.enabled}
      <ViewState kind="blocked" title="Mail is off">
        Turn Mail on in the apps directory, then this page shows here.
        {#snippet action()}
          <a class="link" href={p('/apps/directory')}>Open the apps directory</a>
        {/snippet}
      </ViewState>
    {:else}
      {#if app && app.state !== 'enabled'}
        <Notice tone={stateOf(app).tone === 'error' ? 'error' : 'warn'} title={stateOf(app).label}>
          {app.reason ?? 'Some mail may be missing or stale.'}
          {#snippet action()}
            {#if needs[0]}
              <a class="btn btn--secondary" href={repairHref(needs[0])}>Repair</a>
            {/if}
          {/snippet}
        </Notice>
      {/if}
      {@render children()}
    {/if}
  </Page>
  {@render after?.()}
{/if}
