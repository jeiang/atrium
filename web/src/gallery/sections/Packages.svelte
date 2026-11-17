<script lang="ts">
  import AddPackage from '../../apps/packages/AddPackage.svelte';
  import PackageCard from '../../apps/packages/PackageCard.svelte';
  import '../../apps/packages/packages.css';
  import { quotaLine } from '../../apps/packages/packages.svelte';
  import { seedPackages } from '../../mocks/packages';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const items = seedPackages({});
  const [transit, extracted, exception, out, added, done] = items as [
    (typeof items)[0],
    (typeof items)[0],
    (typeof items)[0],
    (typeof items)[0],
    (typeof items)[0],
    (typeof items)[0],
  ];
  const quota = { used: 7, limit: 10, remaining: 3 };
  const problem = {
    tone: 'error' as const,
    title: 'The outcome is unknown',
    text: 'Ship24 did not confirm the change. atrium reloaded the list and did not try again.',
  };

  function onNothing(): void {}
</script>

<Section title="Packages" data-gallery="packages">
  <p class="pk-quota meta">{quotaLine({ items, quota, ship24: true })}</p>
  <p class="pk-quota meta" data-spent>
    {quotaLine({ items, quota: { used: 10, limit: 10, remaining: 0 }, ship24: true })}
  </p>
  <AddPackage onadded={onNothing} />
  <ul class="pk-list" aria-label="Package states">
    <li><PackageCard item={transit} level={3} ontrack={onNothing} onuntrack={onNothing} ondismiss={onNothing} /></li>
    <li><PackageCard item={extracted} level={3} ontrack={onNothing} onuntrack={onNothing} ondismiss={onNothing} /></li>
    <li>
      <PackageCard
        item={exception}
        level={3}
        {problem}
        ontrack={onNothing}
        onuntrack={onNothing}
        ondismiss={onNothing}
      />
    </li>
    <li><PackageCard item={out} level={3} ontrack={onNothing} onuntrack={onNothing} ondismiss={onNothing} /></li>
    <li>
      <PackageCard
        item={added}
        level={3}
        trackReason="No Ship24 lookups left this month"
        ontrack={onNothing}
        onuntrack={onNothing}
        ondismiss={onNothing}
      />
    </li>
    <li>
      <PackageCard item={done} level={3} offline ontrack={onNothing} onuntrack={onNothing} ondismiss={onNothing} />
    </li>
  </ul>
  <Notice tone="info" title="Ship24 is not connected">
    atrium still finds packages in your mail. Connect Ship24 to follow them from carrier to door.
    {#snippet action()}
      <a class="link" href="#packages">Connect Ship24</a>
    {/snippet}
  </Notice>
  <Notice tone="warn" title="No Ship24 lookups left this month">
    Packages already tracked keep updating. New ones can be tracked when the month turns over.
  </Notice>
  <ViewState kind="empty" title="No packages" level={3}>
    Shipping notices in your mail show up here. Add a tracking number to follow a package yourself.
  </ViewState>
  <Skeleton lines={4} label="Loading packages" />
  <ViewState kind="error" title="Packages did not load" level={3}>
    The package list could not be read.
    {#snippet action()}
      <Button variant="secondary" onclick={onNothing}>Retry</Button>
    {/snippet}
  </ViewState>
  <ViewState kind="offline" title="Cannot reach atrium" level={3}>
    Packages load when atrium answers again. Nothing is sent while offline.
  </ViewState>
</Section>
