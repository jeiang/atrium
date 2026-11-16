<script lang="ts">
  import type { WebSession } from '../contract';
  import Icon from '../shared/ui/Icon.svelte';
  import { chats, settings } from './nav';
  import { p } from './router';
  import { signOut } from './session.svelte';

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
    return [...(root?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])];
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
      await signOut();
    } finally {
      pending = false;
    }
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
    aria-label={`Account: ${name}`}
    onclick={onToggle}
    onkeydown={onKeydown}
  >
    <span class="avatar" aria-hidden="true">{initials || '?'}</span>
    <span class="account__name">{name}</span>
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
