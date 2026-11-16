<script lang="ts">
  import Section from '../../shared/ui/Section.svelte';

  const radii = ['sm', 'md', 'lg', 'pill'] as const;
  const shadows = [1, 2, 3] as const;
</script>

<Section id="g-surfaces" data-gallery="surfaces" title="Surfaces">
  <div class="grid">
    {#each radii as radius (radius)}
      <div class="swatch" style:border-radius="var(--r-{radius})">r-{radius}</div>
    {/each}
  </div>
  <div class="grid">
    {#each shadows as level (level)}
      <div class="swatch" style:border-radius="var(--r-md)" style:box-shadow="var(--shadow-{level})">
        shadow-{level}
      </div>
    {/each}
  </div>
  <hr class="divider">
  <p class="meta">Meta text uses the 13 px step.</p>
</Section>
