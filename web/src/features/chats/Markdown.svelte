<script lang="ts">
  import { type Block, type Inline, parse } from './markdown';

  let { text }: { text: string } = $props();

  const tree = $derived(parse(text));
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
    {:else if node.t === 'link'}
      <a class="link" href={node.href} target="_blank" rel="noopener noreferrer">{@render inlines(node.c)}</a>
    {/if}
  {/each}
{/snippet}

{#snippet blocks(
    list: Block[],
  )}
  {#each list as block, index (index)}
    {#if block.t === 'p'}
      <p>{@render inlines(block.c)}</p>
    {:else if block.t === 'h'}
      <svelte:element this={`h${Math.min(block.level + 2, 6)}`} class="md-h">{@render inlines(block.c)}</svelte:element>
    {:else if block.t === 'code'}
      <pre class="md-code"><code>{block.v}</code></pre>
    {:else if block.t === 'quote'}
      <blockquote>{@render blocks(block.c)}</blockquote>
    {:else if block.t === 'list'}
      {#if block.ordered}
        <ol start={block.start}>
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
      <div class="md-table">
        <table>
          <thead>
            <tr>
              {#each block.head as cell, column (column)}
                <th class={block.align[column] ? `md-${block.align[column]}` : undefined}>{@render inlines(cell)}</th>
              {/each}
            </tr>
          </thead>
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
        </table>
      </div>
    {/if}
  {/each}
{/snippet}

<div class="prose">{@render blocks(tree)}</div>
