<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { lineDiff } from './versions';

  interface Props {
    from: string;
    to: string;
    label: string;
    fromName: string;
    toName: string;
  }

  let { from, to, label, fromName, toName }: Props = $props();

  const lines = $derived(lineDiff(from, to));
  const added = $derived(lines?.filter((line) => line.kind === 'add').length ?? 0);
  const removed = $derived(lines?.filter((line) => line.kind === 'del').length ?? 0);
</script>

<div class="kdiff">
  {#if lines === undefined}
    <p class="kpage__note">These texts are too long to compare line by line.</p>
  {:else if added === 0 && removed === 0}
    <p class="kpage__note">No differences between {fromName} and {toName}.</p>
  {:else}
    <p class="kpage__note">{plural(removed, 'line')} only in {fromName}, {plural(added, 'line')} only in {toName}.</p>
    <ol class="kdiff__lines" aria-label={label}>
      {#each lines as line, index (index)}
        <li class="kdiff__line kdiff__line--{line.kind}">
          <span class="kdiff__mark" aria-hidden="true"
            >{line.kind === 'add' ? '+' : line.kind === 'del' ? '−' : ' '}</span
          >
          {#if line.kind !== 'same'}
            <span class="visually-hidden">{line.kind === 'add' ? `Only in ${toName}:` : `Only in ${fromName}:`}</span>
          {/if}
          <span class="kdiff__text">{line.text || ' '}</span>
        </li>
      {/each}
    </ol>
  {/if}
</div>
