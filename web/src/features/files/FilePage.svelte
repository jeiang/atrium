<script lang="ts">
  import { ApiError } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Markdown from '../../shared/markdown/Markdown.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p, route } from '../../shell/router';
  import { contentUrl, type FileOwner, sizeText, usePreview } from './files';
  import './files.css';

  const ORIGINS: Record<string, string> = { upload: 'Uploaded', generated: 'Generated' };

  function id(raw: string | undefined): number | undefined {
    return raw !== undefined && /^[1-9]\d*$/.test(raw) ? Number(raw) : undefined;
  }

  const owner = $derived.by((): FileOwner | undefined => {
    const space = id(route.params.space);
    if (space !== undefined) return { kind: 'space', id: space };
    const thread = id(route.params.id);
    return thread === undefined ? undefined : { kind: 'thread', id: thread };
  });
  const file = $derived(id(route.params.file));
  const invalid = $derived(owner === undefined || file === undefined);
  const preview = usePreview(
    () => owner,
    () => file,
  );
  const view = $derived(preview.data);
  const offline = $derived(unreachable(preview));
  const missing = $derived(preview.error instanceof ApiError && preview.error.status === 404);
  const back = $derived(
    owner?.kind === 'space'
      ? { href: p('/knowledge/spaces/:space', { params: { space: String(owner.id) } }), label: 'Back to the space' }
      : owner
        ? { href: p('/chat/:id', { params: { id: String(owner.id) } }), label: 'Back to the chat' }
        : { href: p('/knowledge/files'), label: 'All files' },
  );
  const uid = $props.id();

  function onRetry(): void {
    void preview.refetch();
  }
</script>

<article class="fpage">
  <a class="fpage__back link" href={back.href}><Icon name="chevron" />{back.label}</a>
  {#if invalid}
    <h1 class="visually-hidden">File</h1>
    <ViewState kind="empty" title="This file does not exist" level={2}>
      The address does not name a file. Open one from its chat or space.
    </ViewState>
  {:else if offline}
    <h1 class="visually-hidden">File</h1>
    <ViewState kind="offline" title="Cannot reach atrium" level={2}>
      The file loads when the connection returns.
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if preview.isPending}
    <h1 class="visually-hidden">File</h1>
    <ViewState kind="loading" title="Loading the file" />
  {:else if missing}
    <h1 class="visually-hidden">File</h1>
    <ViewState kind="empty" title="This file does not exist" level={2}>
      It may have been deleted. Open another file from its chat or space.
    </ViewState>
  {:else if preview.isError || !view || !owner || file === undefined}
    <h1 class="visually-hidden">File</h1>
    <ViewState kind="error" title="This file could not be loaded" level={2}>
      {preview.error?.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else}
    <header class="fpage__head">
      <p class="eyebrow">{owner.kind === 'space' ? 'Space file' : 'Chat file'}</p>
      <h1 class="page-title fpage__title">{view.file.name}</h1>
      <div class="fpage__meta">
        <Badge>{ORIGINS[view.file.origin] ?? view.file.origin}</Badge>
        <span>{view.file.media_type} · {sizeText(view.file.bytes)} · added {formatDayTime(view.file.created_at)}</span>
      </div>
      <div class="fpage__actions">
        <a class="btn btn--secondary" href={contentUrl(owner, file)} download={view.file.name}>
          <Icon name="downloads" />Download
        </a>
      </div>
    </header>
    {#if view.render === 'image'}
      <img class="fpage__image" src={contentUrl(owner, file)} alt={view.file.name}>
    {:else if view.render === 'markdown'}
      <div class="prose fpage__body"><Markdown text={view.body} /></div>
    {:else if view.render === 'table' && view.rows.length > 0}
      <div class="prose fpage__body">
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <!-- biome-ignore lint/a11y/noNoninteractiveTabindex: a scrolling table needs keyboard access -->
        <section class="md-table" aria-labelledby="{uid}-caption" tabindex="0">
          <table>
            <caption class="visually-hidden" id="{uid}-caption">
              {view.file.name}
            </caption>
            <thead>
              <tr>
                {#each view.rows[0] as cell, index (index)}
                  <th scope="col">{cell}</th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each view.rows.slice(1) as row, index (index)}
                <tr>
                  {#each row as cell, at (at)}
                    <td>{cell}</td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table>
        </section>
      </div>
    {:else if view.render === 'text'}
      <pre class="fpage__text">{view.body}</pre>
    {:else}
      <p class="fpage__note">No preview for this file type. Download it to open it.</p>
    {/if}
    {#if view.truncated}
      <p class="fpage__note" role="note">Showing the first part of the file. Download it to see the rest.</p>
    {/if}
  {/if}
</article>
