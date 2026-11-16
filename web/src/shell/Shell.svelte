<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { Router } from 'sv-router';
  import { announce } from '../shared/announce.svelte';
  import { device } from '../shared/device.svelte';
  import LiveRegion from '../shared/LiveRegion.svelte';
  import Icon from '../shared/ui/Icon.svelte';
  import Toaster from '../shared/ui/Toaster.svelte';
  import { toast } from '../shared/ui/toast.svelte';
  import AccountMenu from './AccountMenu.svelte';
  import { useShell } from './frame';
  import {
    appsDestination,
    chats,
    type DestinationPath,
    destinations,
    home,
    inbox,
    mailStatus,
    moduleOn,
    shownApps,
  } from './nav';
  import { isActive, p, route, routeTitle } from './router';
  import OfflineStatus from './OfflineStatus.svelte';
  import SessionState, { stateTitle } from './SessionState.svelte';
  import { session } from './session.svelte';

  const client = useQueryClient();

  const signedIn = $derived(session.view.status === 'signed-in' ? session.view.session : undefined);
  const cached = $derived(session.view.status === 'signed-in' && session.view.cached === true);
  const abnormal = $derived(signedIn !== undefined && (cached || !device.online));
  const shell = useShell(() => signedIn !== undefined);

  const frame = $derived(signedIn ? shell.data : undefined);
  const waiting = $derived(frame?.needs_you ?? 0);
  const appRows = $derived(shownApps(frame));
  const mailMeta = $derived(mailStatus(frame));
  const visible = $derived(destinations.filter((item) => moduleOn(frame, item.module)));

  $effect(() => {
    if (!signedIn || cached) return;
    let stop: (() => void) | undefined;
    let ended = false;
    Promise.all([import('../contract/events'), import('../shared/live')]).then(
      ([events, live]) => {
        if (!ended) stop = events.startAppEvents({ onNotice: live.topicInvalidator(client) });
      },
      () => {
        if (!ended) toast('Live updates are off. Reload to turn them on.', { tone: 'error' });
      },
    );
    return () => {
      ended = true;
      stop?.();
    };
  });

  const title = $derived(signedIn ? routeTitle() : stateTitle(session.view));

  let lastPath: string | undefined;
  $effect(() => {
    const current = title;
    const path = route.pathname;
    document.title = `${current} · atrium`;
    if (!signedIn) {
      lastPath = undefined;
      return;
    }
    if (lastPath !== undefined && lastPath !== path) {
      announce(current);
      const heading = document.querySelector<HTMLElement>('main h1');
      if (heading) {
        heading.tabIndex = -1;
        heading.focus();
      }
    }
    lastPath = path;
  });

  const current = (path: DestinationPath): 'page' | undefined =>
    (path === '/' ? isActive('/') : isActive.startsWith(path)) ? 'page' : undefined;
  const spacesCurrent = $derived(
    !isActive('/') && !isActive.startsWith('/inbox') && !isActive.startsWith('/chat') ? 'page' : undefined,
  );
</script>

<a class="skip" href="#main">Skip to content</a>
<div class="frame">
  <nav class="rail" aria-label="Main">
    <a class="brand" href={p('/')}><Icon name="home" /><span>atrium</span></a>
    <ul class="rail__list">
      <li>
        <a class="rail-link" href={p('/')} aria-current={current('/')}>
          <Icon name={home.icon} /><span class="rail-link__text">{home.label}</span>
        </a>
      </li>
      <li>
        <a class="rail-link" href={p('/inbox')} aria-current={current('/inbox')}>
          <Icon name={inbox.icon} /><span class="rail-link__text">{inbox.label}</span>
          {#if waiting > 0}
            <span class="rail-link__count" aria-hidden="true">{waiting}</span>
            <span class="visually-hidden">, {waiting} waiting</span>
          {/if}
        </a>
      </li>
      {#each visible.filter((item) => item !== home) as item (item.path)}
        <li>
          <a class="rail-link" href={p(item.path)} aria-current={current(item.path)}>
            <Icon name={item.icon} /><span class="rail-link__text">{item.label}</span>
          </a>
          {#if item === appsDestination && appRows.length > 0}
            <ul class="rail__sub">
              {#each appRows as app (app.id)}
                <li>
                  <a
                    class="rail-link rail-link--sub"
                    href={p('/apps/:app', { params: { app: app.id } })}
                    aria-current={isActive('/apps/:app', { app: app.id }) ? 'page' : undefined}
                  >
                    <Icon name={app.icon} /><span class="rail-link__text">{app.title}</span>
                    {#if app.id === 'mail' && mailMeta}
                      <span class="rail-link__meta">{mailMeta}</span>
                    {/if}
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
    <div class="rail__foot">
      {#if signedIn}
        <AccountMenu session={signedIn} version={frame?.version} placement="rail" />
      {/if}
    </div>
  </nav>
  <header class="topbar">
    <a class="brand" href={p('/')}><Icon name="home" /><span>atrium</span></a>
    <div class="topbar__account">
      {#if signedIn}
        <AccountMenu session={signedIn} version={frame?.version} placement="topbar" />
      {/if}
    </div>
  </header>
  <main id="main" class="main" tabindex="-1">
    <div class="status-slot" role="status">
      {#if abnormal}<OfflineStatus {cached} />{/if}
    </div>
    {#if signedIn && shell.isPending}
      <p class="frame-note">Loading atrium. Home, your plan and the inbox appear here in a moment.</p>
    {:else if signedIn}
      <Router />
    {:else}
      <SessionState view={session.view} />
    {/if}
  </main>
  <nav class="tabbar" aria-label="Tabs">
    <a href={p('/')} aria-current={current('/')}><Icon name={home.icon} /><span>{home.label}</span></a>
    <a href={p('/inbox')} aria-current={current('/inbox')}>
      <Icon name={inbox.icon} /><span>Decide</span>
      {#if waiting > 0}
        <span class="tabbar__count" aria-hidden="true">{waiting}</span>
        <span class="visually-hidden">, {waiting} waiting</span>
      {/if}
    </a>
    <a href={p('/chat')} aria-current={current('/chat')}><Icon name={chats.icon} /><span>{chats.label}</span></a>
    <a href={p('/spaces')} aria-current={spacesCurrent}><Icon name="apps" /><span>Spaces</span></a>
  </nav>
</div>
<LiveRegion />
<Toaster />
