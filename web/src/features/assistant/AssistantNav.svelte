<script lang="ts">
  import { p } from '../../shell/router';
  import type { AssistantView } from './assistant';

  let { current }: { current: AssistantView } = $props();

  const links = [
    { key: 'routines', href: p('/assistant/routines'), label: 'Routines' },
    { key: 'authority', href: p('/assistant/authority'), label: 'Authority' },
    { key: 'spend', href: p('/assistant/spend'), label: 'Spend' },
    { key: 'behavior', href: p('/assistant/behavior'), label: 'Behavior' },
  ] as const;
</script>

<nav class="asst-nav" aria-label="Assistant">
  {#each links as link (link.key)}
    <a class="asst-nav__link" href={link.href} aria-current={current === link.key ? 'page' : undefined}>{link.label}</a>
  {/each}
</nav>
