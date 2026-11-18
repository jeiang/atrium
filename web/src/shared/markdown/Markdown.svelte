<script lang="ts">
  import Icon from '../ui/Icon.svelte';
  import { loadedParser, loadMarkdown } from './load';
  import './markdown.css';
  import type { Block, Inline } from './tree';

  let { text, titles }: { text: string; titles?: ReadonlyMap<number, string> } = $props();

  let parse = $state(loadedParser());
  if (!loadedParser()) {
    loadMarkdown().then(
      (loaded) => {
        parse = loaded;
      },
      () => undefined,
    );
  }

  const tree = $derived(parse?.(text));
</script>

{#snippet inlines(
    list: Inline[],
  )}
  {#each list as node, index (index)}
    {#if node.t === 'text'}
      {node.v}
    {:else if node.t === 'code'}
      <code>{node.v}</code>
    {:else if node.t === 'br'}
      <br>
    {:else if node.t === 'strong'}
      <strong>{@render inlines(node.c)}</strong>
    {:else if node.t === 'em'}
      <em>{@render inlines(node.c)}</em>
    {:else if node.t === 'del'}
      <del>{@render inlines(node.c)}</del>
    {:else if node.t === 'link' && node.external}
      <a href={node.href} title={node.title} target="_blank" rel="noopener noreferrer"
        >{@render inlines(node.c)}<Icon name="external" /><span class="visually-hidden"> (new tab)</span></a
      >
    {:else if node.t === 'link'}
      <a href={node.href} title={node.title} rel="noopener noreferrer"
        >{#if node.cite && titles?.get(node.cite)}
          {titles.get(node.cite)}
        {:else}
          {@render inlines(node.c)}
        {/if}</a
      >
    {/if}
  {/each}
{/snippet}

{#snippet blocks(
    list: Block[],
  )}
  {#each list as block, index (index)}
    {#if block.t === 'p'}
      {#if block.bare}
        {@render inlines(block.c)}
      {:else}
        <p>{@render inlines(block.c)}</p>
      {/if}
    {:else if block.t === 'raw'}
      <p class="md-raw">{block.v}</p>
    {:else if block.t === 'h'}
      <svelte:element this={`h${Math.min(block.level + 2, 6)}`} class="md-h">{@render inlines(block.c)}</svelte:element>
    {:else if block.t === 'code'}
      <div class="md-code">
        {#if block.lang}
          <span class="md-lang">{block.lang}</span>
        {/if}
        <pre><code>{block.v}</code></pre>
      </div>
    {:else if block.t === 'quote'}
      <blockquote>{@render blocks(block.c)}</blockquote>
    {:else if block.t === 'list'}
      {#if block.ordered}
        <ol start={block.start === 1 ? undefined : block.start}>
          {#each block.items as entry, position (position)}
            <li>{@render blocks(entry)}</li>
          {/each}
        </ol>
      {:else}
        <ul>
          {#each block.items as entry, position (position)}
            <li>{@render blocks(entry)}</li>
          {/each}
        </ul>
      {/if}
    {:else if block.t === 'hr'}
      <hr>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <!-- biome-ignore lint/a11y/noNoninteractiveTabindex: a scrolling table needs keyboard access -->
      <section class="md-table" aria-label="Table" tabindex="0">
        <table>
          <thead>
            <tr>
              {#each block.head as cell, column (column)}
                <th class={block.align[column] ? `md-${block.align[column]}` : undefined}>{@render inlines(cell)}</th>
              {/each}
            </tr>
          </thead>
          {#if block.rows.length > 0}
            <tbody>
              {#each block.rows as row, line (line)}
                <tr>
                  {#each block.head as _, column (column)}
                    <td class={block.align[column] ? `md-${block.align[column]}` : undefined}>
                      {@render inlines(row[column] ?? [])}
                    </td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          {/if}
        </table>
      </section>
    {/if}
  {/each}
{/snippet}

<div class="prose">
  {#if tree}
    {@render blocks(tree)}
  {:else}
    <p class="md-raw">{text}</p>
  {/if}
</div>
