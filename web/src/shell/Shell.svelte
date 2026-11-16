<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { Router } from 'sv-router';
  import { authIdentity, ok } from '../contract';
  import { announce } from '../shared/announce.svelte';
  import LiveRegion from '../shared/LiveRegion.svelte';
  import { isActive, p, pages, route, routeTitle } from './router';

  const mock = import.meta.env.MODE === 'mock';

  const identity = createQuery(() => ({
    queryKey: ['auth.identity'],
    queryFn: async ({ signal }) => ok(await authIdentity({ signal })),
  }));

  const status = $derived(
    identity.isPending ? 'Connecting' : identity.isSuccess ? `Signed in as ${identity.data.name}` : 'Not connected',
  );

  let announced = false;
  $effect(() => {
    const title = routeTitle(route.pathname);
    document.title = `${title} · atrium`;
    if (announced) {
      announce(title);
      const heading = document.querySelector<HTMLElement>('main h1');
      if (heading) {
        heading.tabIndex = -1;
        heading.focus();
      }
    }
    announced = true;
  });
</script>

<div class="shell">
  <header class="bar">
    <span class="brand">atrium</span>
    <nav aria-label="Main">
      <ul class="nav">
        {#each pages as page (page.path)}
          <li>
            <a href={p(page.path)} aria-current={isActive(page.path) ? 'page' : undefined}>{page.title}</a>
          </li>
        {/each}
      </ul>
    </nav>
    <span class="status" data-state={identity.status}>
      {status}
      {#if mock}
        <span class="mode">mock</span>
      {/if}
    </span>
  </header>
  <main class="main">
    <Router />
  </main>
</div>
<LiveRegion />
