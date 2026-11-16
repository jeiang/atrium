<script lang="ts">
  import AppCard from '../../features/apps/AppCard.svelte';
  import type { Outcome } from '../../features/apps/apps';
  import '../../features/apps/apps.css';
  import { entries, seedApps } from '../../mocks/apps';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const all = entries(seedApps(), '1');
  const mailOff = entries(
    seedApps().map((app) => (app.id === 'mail' ? { ...app, enabled: false } : app)),
    '1',
  );
  const blocked = mailOff.filter((app) => app.id === 'parcels');
  const grafana = all.filter((app) => app.id === 'grafana');
  const refusal: Outcome = {
    tone: 'warn',
    title: 'Another app needs it',
    text: 'Parcels needs Mail. Turn it off first.',
    open: [],
  };
  const stale: Outcome = {
    tone: 'warn',
    title: 'This changed since it loaded',
    text: 'atrium reloaded the apps. Switch again if you still want the change.',
    open: [],
  };
  const mail = all.filter((app) => app.id === 'mail');
  const open = all.filter((app) => app.enabled);

  function onSwitch(): void {}
</script>

<Section id="g-apps" data-gallery="apps" title="Apps">
  <ul class="app-grid" aria-label="Directory cards">
    {#each all as app (app.id)}
      <li><AppCard {app} onswitch={onSwitch} /></li>
    {/each}
    {#each blocked as app (app.id)}
      <li><AppCard {app} onswitch={onSwitch} /></li>
    {/each}
    {#each grafana as app (app.id)}
      <li><AppCard {app} pending target onswitch={onSwitch} /></li>
      <li><AppCard {app} outcome={stale} onswitch={onSwitch} /></li>
    {/each}
    {#each mail as app (app.id)}
      <li><AppCard {app} outcome={refusal} onswitch={onSwitch} /></li>
    {/each}
  </ul>
  <ul class="app-grid" aria-label="Hub entries">
    {#each open as app (app.id)}
      <li><AppCard {app} links /></li>
    {/each}
  </ul>
  <div class="grid">
    <ViewState kind="loading" title="Loading the apps" />
    <ViewState kind="empty" title="No apps are on">Turn one on in the directory.</ViewState>
    <ViewState kind="error" title="The apps did not load">The app states are not evaluated yet.</ViewState>
    <ViewState kind="offline" title="Cannot reach atrium">The apps load when atrium answers again.</ViewState>
  </div>
</Section>
