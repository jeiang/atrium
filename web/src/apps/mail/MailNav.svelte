<script lang="ts">
  import type { AppEntry } from '../../contract';
  import { navLinks } from '../../shell/manifest';
  import { p } from '../../shell/router';

  let { app, current }: { app: AppEntry | undefined; current?: string } = $props();

  const links = $derived(app ? navLinks(app) : [{ label: 'Messages', href: p('/apps/mail') }]);
</script>

<nav class="mail-nav" aria-label="Mail">
  {#each links as link (link.href)}
    <a class="mail-nav__link" href={link.href} aria-current={current === link.href ? 'page' : undefined}>
      {link.label}
    </a>
  {/each}
</nav>
