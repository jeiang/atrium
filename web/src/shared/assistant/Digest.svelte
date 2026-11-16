<script lang="ts">
  import { plural, webHref } from './format';
  import type { DigestLine } from './types';
  import './assistant.css';

  interface Props {
    lines: DigestLine[] | null;
    title?: string;
    limit?: number;
    empty?: string;
  }

  let { lines, title = 'Digest', limit = 5, empty = 'Nothing new since the last digest.' }: Props = $props();

  const uid = $props.id();
  const list = $derived(lines ?? []);
  const shown = $derived(list.slice(0, limit).map((line) => ({ text: line.text, link: href(line.url) })));
  const rest = $derived(list.length - shown.length);

  function href(url: string | undefined): string | undefined {
    if (!url) return undefined;
    if (url.startsWith('/') && !url.startsWith('//')) return url;
    return webHref(url);
  }
</script>

<section class="digest" aria-labelledby="{uid}-title">
  <div class="digest__head">
    <h3 class="card__title" id="{uid}-title">{title}</h3>
    <span class="meta">{plural(list.length, 'item')}</span>
  </div>
  {#if shown.length === 0}
    <p class="digest__more">{empty}</p>
  {:else}
    <ol class="digest__list">
      {#each shown as line, index (index)}
        <li>
          {#if line.link}
            <a class="link" href={line.link}>{line.text}</a>
          {:else}
            <span>{line.text}</span>
          {/if}
        </li>
      {/each}
    </ol>
    {#if rest > 0}
      <p class="digest__more">and {rest} more</p>
    {/if}
  {/if}
</section>
