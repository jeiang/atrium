<script lang="ts">
  import { plural, webHref } from './format';
  import type { DigestLine } from './types';
  import './assistant.css';

  interface Props {
    lines: DigestLine[] | null;
    title?: string;
    limit?: number;
    empty?: string;
    more?: string;
    level?: 2 | 3;
  }

  let {
    lines,
    title = 'Digest',
    limit = 5,
    empty = 'Nothing new since the last digest.',
    more,
    level = 3,
  }: Props = $props();

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
    <svelte:element this={`h${level}`} class="card__title" id="{uid}-title">{title}</svelte:element>
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
      <p class="digest__more">
        {#if more}
          <a class="link" href={more}>and {rest} more</a>
        {:else}
          and {rest} more
        {/if}
      </p>
    {/if}
  {/if}
</section>
