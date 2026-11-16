<script lang="ts" module>
  import type { IconName } from '../shared/ui/Icon.svelte';

  export interface Tile {
    key: string;
    href: string;
    label: string;
    icon: IconName;
    meta?: string;
  }
</script>

<script lang="ts">
  import Icon from '../shared/ui/Icon.svelte';

  let { tiles, label }: { tiles: Tile[]; label: string } = $props();
</script>

<ul class="tiles" aria-label={label}>
  {#each tiles as tile (tile.key)}
    <li>
      <a class="tile" href={tile.href}>
        <Icon name={tile.icon} />
        <span class="tile__label">{tile.label}</span>
        {#if tile.meta}
          <span class="meta">{tile.meta}</span>
        {/if}
      </a>
    </li>
  {/each}
</ul>
