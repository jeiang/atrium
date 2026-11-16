<script lang="ts">
  import LineDiff from '../../features/knowledge/LineDiff.svelte';
  import NeighborhoodMap, { type Neighbor } from '../../features/knowledge/NeighborhoodMap.svelte';
  import '../../features/knowledge/knowledge.css';
  import Section from '../../shared/ui/Section.svelte';

  function onopen(id: number): void {
    location.hash = `subject-${id}`;
  }

  const center = { id: 21, title: 'Ilse de Wit', kind: 'wiki', confirmed: true, private: false };
  const neighbors: Neighbor[] = [
    {
      node: { id: 23, title: 'Garden weather station', kind: 'wiki', confirmed: false, private: false },
      side: 'in',
      kinds: ['see_also'],
      words: 'links here: see also',
    },
    {
      node: { id: 22, title: 'Shared garden project', kind: 'wiki', confirmed: true, private: false },
      side: 'out',
      kinds: ['works_with'],
      words: 'linked from this subject: works with',
    },
    {
      node: { id: 24, title: 'Water the tomatoes in the morning', kind: 'lesson', confirmed: true, private: true },
      side: 'out',
      kinds: ['mentions'],
      words: 'linked from this subject: mentioned',
    },
    {
      node: { id: 26, title: 'Seed order for next spring', kind: 'wiki', confirmed: true, private: false },
      side: 'out',
      kinds: ['same_run'],
      words: 'linked from this subject: written in the same chat',
    },
  ];
</script>

<Section id="g-knowledge-map" data-gallery="knowledge-map" title="Subject map">
  <p class="kmap__phone-note">Below 861 px the map page shows only its list.</p>
  <div class="kmap__canvas kmap__canvas--wide">
    <NeighborhoodMap {center} {neighbors} centerHref="#g-knowledge-map" href={(id) => `#subject-${id}`} {onopen} />
  </div>
</Section>

<Section id="g-line-diff" data-gallery="line-diff" title="Line comparison">
  <LineDiff
    from={'Write in plain words.\nAsk before sending.'}
    to={'Write in plain words.\nAsk before sending anything on my behalf.\nKeep answers short.'}
    label="Differences from the earlier text to the current text"
    fromName="the earlier text"
    toName="the current text"
  />
</Section>
