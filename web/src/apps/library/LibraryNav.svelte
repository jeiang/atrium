<script lang="ts">
  import { p } from '../../shell/router';
  import type { View } from './library';

  let { current }: { current: View } = $props();

  const links = [
    { key: 'catalogue', href: p('/apps/media'), label: 'Library' },
    { key: 'following', href: p('/apps/media/following'), label: 'Following' },
    { key: 'intake', href: p('/apps/media/intake'), label: 'Intake' },
    { key: 'downloads', href: p('/apps/torrents'), label: 'Downloads' },
  ] as const;
</script>

<nav class="lib-nav" aria-label="Library">
  {#each links as link (link.key)}
    <a class="lib-nav__link" href={link.href} aria-current={current === link.key ? 'page' : undefined}>{link.label}</a>
  {/each}
</nav>
