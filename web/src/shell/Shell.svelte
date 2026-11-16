<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { Router } from 'sv-router';
  import { ApiError, automationResume, ok } from '../contract';
  import { announce } from '../shared/announce.svelte';
  import { device } from '../shared/device.svelte';
  import LiveRegion from '../shared/LiveRegion.svelte';
  import Button from '../shared/ui/Button.svelte';
  import Icon from '../shared/ui/Icon.svelte';
  import Notice from '../shared/ui/Notice.svelte';
  import Toaster from '../shared/ui/Toaster.svelte';
  import { toast } from '../shared/ui/toast.svelte';
  import AccountMenu from './AccountMenu.svelte';
  import { useShell } from './frame';
  import { openApps, useApps } from './manifest';
  import { appsDestination, chats, type DestinationPath, destinations, home, inbox, mailStatus, moduleOn } from './nav';
  import OfflineStatus from './OfflineStatus.svelte';
  import { isActive, navigate, p, route, routeTitle } from './router';
  import SessionState, { stateTitle } from './SessionState.svelte';
  import { session } from './session.svelte';

  const client = useQueryClient();

  const signedIn = $derived(session.view.status === 'signed-in' ? session.view.session : undefined);
  const cached = $derived(session.view.status === 'signed-in' && session.view.cached === true);
  const abnormal = $derived(signedIn !== undefined && (cached || !device.online));
  const shell = useShell(() => signedIn !== undefined);

  const frame = $derived(signedIn ? shell.data : undefined);
  const waiting = $derived(frame?.needs_you ?? 0);
  const apps = useApps(() => signedIn !== undefined);
  const appRows = $derived(signedIn ? openApps(apps.data) : []);
  const paused = $derived(frame?.pause?.paused === true);
  let resuming = $state(false);
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

  const title = $derived(
    signedIn
      ? routeTitle((id) => apps.data?.find((app) => app.id === id && app.enabled)?.name)
      : stateTitle(session.view),
  );

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
        heading.focus({ preventScroll: true });
      }
    }
    lastPath = path;
  });

  const current = (path: DestinationPath): 'page' | undefined =>
    (path === '/' ? isActive('/') : isActive.startsWith(path)) ? 'page' : undefined;
  const appsCurrent = $derived(
    !isActive('/') && !isActive.startsWith('/inbox') && !isActive.startsWith('/chat') && !isActive('/search')
      ? 'page'
      : undefined,
  );
  const shortcut = navigator.userAgent.includes('Mac') ? '⌘K' : 'Ctrl K';

  async function onResume(): Promise<void> {
    if (resuming) return;
    resuming = true;
    try {
      ok(await automationResume({ headers: { 'Idempotency-Key': crypto.randomUUID() } }));
      announce('Automation resumed');
    } catch (error) {
      toast(
        error instanceof ApiError
          ? error.message
          : 'The outcome is unknown. atrium reloaded the pause state and did not try again.',
        { tone: 'error' },
      );
    } finally {
      resuming = false;
      void client.invalidateQueries({ queryKey: ['shell'] });
    }
  }

  function onShortcut(event: KeyboardEvent): void {
    if (!signedIn || event.altKey || event.shiftKey || !(event.metaKey || event.ctrlKey)) return;
    if (event.key.toLowerCase() !== 'k') return;
    event.preventDefault();
    const input = document.querySelector<HTMLInputElement>('[data-search-input]');
    if (input) {
      input.focus();
      input.select();
      return;
    }
    void navigate('/search');
  }
</script>

<a class="skip" href="#main">Skip to content</a>
<div class="frame">
  <nav class="rail" aria-label="Main">
    <a class="brand" href={p('/')}><Icon name="home" /><span>atrium</span></a>
    <a
      class="rail-link rail-search"
      href={p('/search')}
      aria-current={isActive('/search') ? 'page' : undefined}
      aria-keyshortcuts="Meta+K Control+K"
    >
      <Icon name="search" /><span class="rail-link__text">Search</span>
      <kbd class="rail-search__key" aria-hidden="true">{shortcut}</kbd>
    </a>
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
                    <span class="rail-link__text">{app.name}</span>
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
    <a
      class="topbar__search"
      href={p('/search')}
      aria-label="Search"
      aria-current={isActive('/search') ? 'page' : undefined}
      aria-keyshortcuts="Meta+K Control+K"
      ><Icon name="search" /></a
    >
    <div class="topbar__account">
      {#if signedIn}
        <AccountMenu session={signedIn} version={frame?.version} placement="topbar" />
      {/if}
    </div>
  </header>
  <main id="main" class="main" tabindex="-1">
    <div class="status-slot" role="status">
      {#if abnormal}
        <OfflineStatus {cached} />
      {/if}
    </div>
    {#if signedIn && paused}
      <div class="pause-slot">
        <Notice tone="warn" title="Automation paused">
          {frame?.pause?.reason || 'Scheduled work is skipped until you resume.'}
          {#snippet action()}
            <Button variant="secondary" pending={resuming} onclick={onResume}>Resume</Button>
          {/snippet}
        </Notice>
      </div>
    {/if}
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
    <a href={p('/apps')} aria-current={appsCurrent}><Icon name="apps" /><span>Apps</span></a>
  </nav>
</div>
<svelte:window onkeydown={onShortcut} />
<LiveRegion />
<Toaster />
