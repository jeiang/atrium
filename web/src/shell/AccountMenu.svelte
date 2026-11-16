<script lang="ts">
  import type { WebSession } from '../contract';
  import { device, setForget } from '../shared/device.svelte';
  import { queryClient } from '../shared/query';
  import { restore } from '../shared/readcache';
  import Icon from '../shared/ui/Icon.svelte';
  import { toast } from '../shared/ui/toast.svelte';
  import { chats, settings } from './nav';
  import { p } from './router';
  import { session as current, handleSignOut } from './session.svelte';

  interface Props {
    session: WebSession;
    version?: string;
    placement: 'rail' | 'topbar';
  }

  let { session, version, placement }: Props = $props();

  const id = $props.id();
  let open = $state(false);
  let pending = $state(false);
  let root = $state<HTMLElement>();
  let button = $state<HTMLButtonElement>();

  const name = $derived(session.identity.name || session.identity.email || session.identity.subject);
  const initials = $derived(
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((part) => part.charAt(0).toUpperCase())
      .join(''),
  );

  function items(): HTMLElement[] {
    return [...(root?.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemcheckbox"]') ?? [])];
  }

  function close(refocus: boolean): void {
    open = false;
    if (refocus) button?.focus();
  }

  function onToggle(): void {
    open = !open;
    if (open) requestAnimationFrame(() => items()[0]?.focus());
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!open) return;
    const list = items();
    const index = list.indexOf(document.activeElement as HTMLElement);
    const moves: Record<string, number> = {
      ArrowDown: (index + 1) % list.length,
      ArrowUp: (index - 1 + list.length) % list.length,
      Home: 0,
      End: list.length - 1,
    };
    if (event.key === 'Escape') {
      event.preventDefault();
      close(true);
    } else if (event.key === 'Tab') {
      close(false);
    } else if (event.key in moves) {
      event.preventDefault();
      list[moves[event.key] ?? 0]?.focus();
    }
  }

  function onWindowPointer(event: PointerEvent): void {
    if (open && root && !root.contains(event.target as Node)) close(false);
  }

  async function onSignOut(): Promise<void> {
    pending = true;
    try {
      await handleSignOut();
    } finally {
      pending = false;
    }
  }

  function onForget(): void {
    const on = !device.forget;
    const verified = current.view.status === 'signed-in' && !current.view.cached;
    setForget(on);
    if (on) toast('Data on this device deleted.');
    else void restore(queryClient, session, verified);
  }
</script>

<svelte:window onpointerdown={onWindowPointer} />

<div class="account" data-placement={placement} bind:this={root}>
  <button
    bind:this={button}
    class="account__button"
    type="button"
    aria-haspopup="menu"
    aria-expanded={open}
    aria-controls={id}
    onclick={onToggle}
    onkeydown={onKeydown}
  >
    <span class="avatar" aria-hidden="true">{initials || '?'}</span>
    <span class="account__name" aria-hidden="true">{name}</span>
    <span class="visually-hidden">Account: {name}</span>
  </button>
  <div class="account__popup" {id} hidden={!open}>
    <p class="account__who">
      <span class="account__who-name">{name}</span>
      {#if session.identity.email && session.identity.email !== name}
        <span class="meta">{session.identity.email}</span>
      {/if}
    </p>
    <div class="account__menu" role="menu" aria-label="Account" tabindex="-1" onkeydown={onKeydown}>
      <a role="menuitem" class="account__item" href={p(chats.path)} onclick={() => close(false)}>
        <Icon name={chats.icon} /><span>{chats.label}</span>
      </a>
      <a role="menuitem" class="account__item" href={p(settings.path)} onclick={() => close(false)}>
        <Icon name={settings.icon} /><span>{settings.label}</span>
      </a>
      <button
        role="menuitemcheckbox"
        class="account__item"
        type="button"
        aria-checked={device.forget}
        onclick={onForget}
      >
        <span class="account__check" aria-hidden="true">
          {#if device.forget}
            <Icon name="check" />
          {/if}
        </span>
        <span>Don't keep data on this device</span>
      </button>
      {#if session.sign_out}
        <button
          role="menuitem"
          class="account__item"
          type="button"
          aria-busy={pending}
          disabled={pending}
          onclick={onSignOut}
        >
          <Icon name="signout" /><span>Sign out</span>
        </button>
      {/if}
    </div>
    {#if version}
      <p class="account__version meta">atrium {version}</p>
    {/if}
  </div>
</div>
