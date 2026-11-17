<script lang="ts">
  import Icon from '../../shared/ui/Icon.svelte';
  import type { AppEntry } from '../../contract';
  import { navLinks } from '../../shell/manifest';
  import { p } from '../../shell/router';

  let { app, current }: { app: AppEntry | undefined; current?: string } = $props();

  const links = $derived(app ? navLinks(app) : [{ label: 'Messages', href: p('/apps/mail'), leaves: false }]);
</script>

<nav class="mail-nav" aria-label="Mail">
  {#each links as link (link.href)}
    <a class="mail-nav__link" href={link.href} aria-current={current === link.href ? 'page' : undefined}>
      {link.label}
      {#if link.leaves}
        <Icon name="external" /><span class="visually-hidden">, opens the current interface</span>
      {/if}
    </a>
  {/each}
</nav>
