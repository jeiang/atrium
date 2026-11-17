<script lang="ts">
  import { p } from '../../shell/router';

  let { current, todo, waiting }: { current: 'todo' | 'waiting' | 'tasks'; todo?: number; waiting?: number } = $props();

  const links = $derived([
    { key: 'todo', href: p('/plan'), label: 'To do', count: todo },
    { key: 'waiting', href: p('/plan/waiting'), label: 'Waiting', count: waiting },
    { key: 'tasks', href: p('/plan/tasks'), label: 'Tasks', count: undefined },
  ]);
</script>

<nav class="plan-nav" aria-label="Plan">
  {#each links as link (link.key)}
    <a class="plan-nav__link" href={link.href} aria-current={current === link.key ? 'page' : undefined}>
      {link.label}
      {#if link.count !== undefined}
        <span class="plan-nav__count">{link.count}</span>
      {/if}
    </a>
  {/each}
</nav>
