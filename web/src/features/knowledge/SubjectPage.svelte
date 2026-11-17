<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { on } from 'svelte/events';
  import { ApiError, type KnowledgeFact, type KnowledgeHistory, type KnowledgeLink } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { formatClock, formatDay, formatDayTime, plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Markdown from '../../shared/markdown/Markdown.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p } from '../../shell/router';
  import {
    batchText,
    dialogOpen,
    failureText,
    handleConfirm,
    handlePurge,
    handleRemoveLink,
    handleRestore,
    handleRetract,
    handleRetractRun,
    handleSaveEdit,
    handleUndoEffects,
    isStale,
    KINDS,
    liveLinks,
    noteMapOpened,
    plainKey,
    refreshKnowledge,
    relationLabel,
    takeMapClosed,
    useHistory,
    useLinks,
    useSubject,
    writerLabel,
  } from './knowledge';
  import LinkDialog from './LinkDialog.svelte';

  interface Props {
    id: number;
    phone: boolean;
  }

  let { id, phone }: Props = $props();

  const client = useQueryClient();
  const page = useSubject(() => id);
  const links = useLinks(() => id);
  const history = useHistory(() => id);

  let editing = $state(false);
  let draftTitle = $state('');
  let draftBody = $state('');
  let base = $state('');
  let saveState = $state<'idle' | 'saving' | 'saved'>('idle');
  let savedAt = $state<string>();
  let staleNote = $state(false);
  let saveError = $state<string>();
  let busy = $state<string>();
  let removing = $state<number>();
  let linking = $state(false);
  let mapButton = $state<HTMLButtonElement>();
  let purging = $state(false);
  let purgeBusy = $state(false);
  let purgeFailed = $state<string>();
  let runConfirm = $state(false);
  let runBusy = $state(false);
  let runFailed = $state<string>();

  const data = $derived(page.data);
  const subject = $derived(data?.subject);
  const outgoing = $derived(liveLinks(links.data?.outgoing));
  const incoming = $derived(liveLinks(links.data?.incoming));
  const offline = $derived(unreachable(page));
  const missing = $derived(page.error instanceof ApiError && page.error.status === 404);
  const sameRun = $derived(history.data?.same_run ?? []);
  const runId = $derived(subject?.source?.run_id);
  const runCount = $derived(sameRun.length + 1);

  $effect(() => {
    void id;
    editing = false;
    staleNote = false;
    saveError = undefined;
    saveState = 'idle';
  });

  $effect(() => {
    if (!data || !mapButton || !takeMapClosed(id)) return;
    const button = mapButton;
    const timer = setTimeout(() => button.focus());
    return () => clearTimeout(timer);
  });

  $effect(() =>
    on(window, 'keydown', (event) => {
      if (event.key.toLowerCase() !== 'm' || !plainKey(event) || editing || !data || dialogOpen()) return;
      event.preventDefault();
      onMap();
    }),
  );

  function onMap(): void {
    noteMapOpened(id);
    void navigate('/knowledge/map', { search: { subject: String(id) } });
  }

  function stateTag(record: { confirmed: boolean; retracted?: boolean; superseded?: boolean }): {
    label: string;
    tone: 'ok' | 'warn' | 'error' | 'neutral';
  } {
    if (record.retracted) return { label: 'Retracted', tone: 'error' };
    if (record.superseded) return { label: 'Replaced', tone: 'neutral' };
    return record.confirmed ? { label: 'Confirmed', tone: 'ok' } : { label: 'Unconfirmed', tone: 'warn' };
  }

  function factMeta(fact: KnowledgeFact): string {
    const parts = [fact.value, `from ${writerLabel(fact.writer)}`, `learned ${formatDay(fact.learned_at)}`];
    if (fact.updated_at !== fact.learned_at) parts.push(`updated ${formatDay(fact.updated_at)}`);
    return parts.filter(Boolean).join(' · ');
  }

  function linkMeta(link: KnowledgeLink, end: KnowledgeLink['target']): string {
    const parts = [
      relationLabel(link.relation),
      end.kind === 'record' ? (KINDS[end.record_kind ?? ''] ?? 'Knowledge') : end.kind,
    ];
    if (end.state !== 'live') parts.push(end.state);
    parts.push(`added by ${writerLabel(link.writer)}`);
    return parts.join(' · ');
  }

  function endHref(end: KnowledgeLink['target']): string | undefined {
    if (end.kind === 'record') return p('/knowledge/:id', { params: { id: end.id } });
    if (end.kind === 'task') return p('/plan/tasks/:id', { params: { id: end.id } });
    if (end.kind === 'space' && end.state !== 'missing')
      return p('/knowledge/spaces/:space', { params: { space: end.id } });
    return undefined;
  }

  function historyLabel(entry: KnowledgeHistory): string {
    const tool = entry.tool.toLowerCase();
    const verb = tool.includes('retract')
      ? 'Retracted'
      : tool.includes('restore')
        ? 'Restored'
        : tool.includes('confirm')
          ? 'Confirmed'
          : tool.includes('link')
            ? 'Linked'
            : tool.includes('create') || tool.includes('remember')
              ? 'Written'
              : 'Edited';
    return `${verb} by ${writerLabel(entry.by)}`;
  }

  function failure(error: unknown): string {
    return failureText(error);
  }

  function onEdit(): void {
    if (!subject) return;
    draftTitle = subject.title;
    draftBody = subject.body;
    base = subject.updated_at;
    staleNote = false;
    saveError = undefined;
    saveState = 'idle';
    editing = true;
  }

  function onCancel(): void {
    editing = false;
    staleNote = false;
    saveError = undefined;
  }

  async function handleSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (saveState === 'saving') return;
    if (!draftTitle.trim()) {
      saveError = 'Give the page a title.';
      return;
    }
    saveState = 'saving';
    saveError = undefined;
    try {
      await handleSaveEdit(id, base, { title: draftTitle.trim(), body: draftBody });
      saveState = 'saved';
      savedAt = formatClock(new Date());
      staleNote = false;
      editing = false;
      announce('Saved.');
      await refreshKnowledge(client);
    } catch (error) {
      saveState = 'idle';
      if (isStale(error)) {
        const fresh = await page.refetch();
        void client.invalidateQueries({ queryKey: ['knowledge', 'detail', id] });
        base = fresh.data?.subject.updated_at ?? base;
        staleNote = true;
        announce('This page changed since you opened it. It has been reloaded.');
        return;
      }
      saveError = failure(error);
    }
  }

  async function handleConfirmPage(): Promise<void> {
    busy = 'confirm';
    try {
      await handleConfirm(id);
      toast('Confirmed.');
      await refreshKnowledge(client);
    } catch (error) {
      toast(failure(error), { tone: 'error' });
    } finally {
      busy = undefined;
    }
  }

  async function handleUndo(effects: readonly number[], done: string): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast(done);
    } catch (error) {
      toast(failure(error), { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function handleRetractPage(): Promise<void> {
    if (!subject) return;
    const title = subject.title;
    busy = 'retract';
    try {
      const change = await handleRetract(id);
      const effects = change.effect_ids ?? [];
      toast(`Retracted: ${title}. Kept in history.`, {
        action:
          effects.length > 0
            ? { label: 'Undo', run: () => void handleUndo(effects, `Restored: ${title}.`) }
            : undefined,
      });
      await refreshKnowledge(client);
    } catch (error) {
      toast(failure(error), { tone: 'error' });
    } finally {
      busy = undefined;
    }
  }

  async function handleRestorePage(): Promise<void> {
    if (!subject) return;
    busy = 'restore';
    try {
      await handleRestore(id);
      toast(`Restored: ${subject.title}.`);
      await refreshKnowledge(client);
    } catch (error) {
      toast(failure(error), { tone: 'error' });
    } finally {
      busy = undefined;
    }
  }

  async function handleRemove(link: KnowledgeLink, end: KnowledgeLink['target']): Promise<void> {
    removing = link.id;
    try {
      const change = await handleRemoveLink(link.id);
      const effects = change.effect_ids ?? [];
      toast(`Removed the link to ${end.title}.`, {
        action:
          effects.length > 0
            ? { label: 'Undo', run: () => void handleUndo(effects, `Linked to ${end.title} again.`) }
            : undefined,
      });
      await refreshKnowledge(client);
    } catch (error) {
      toast(failure(error), { tone: 'error' });
    } finally {
      removing = undefined;
    }
  }

  function onAskPurge(): void {
    purgeFailed = undefined;
    purging = true;
  }

  async function onPurge(): Promise<void> {
    if (purgeBusy || !subject) return;
    const title = subject.title || 'Untitled';
    purgeBusy = true;
    purgeFailed = undefined;
    try {
      await handlePurge(id);
      purging = false;
      toast(`Deleted for good: ${title}.`);
      await navigate(phone ? '/knowledge/subjects' : '/knowledge');
      await refreshKnowledge(client);
    } catch (error) {
      purgeFailed = failure(error);
    } finally {
      purgeBusy = false;
    }
  }

  function onAskRetractRun(): void {
    runFailed = undefined;
    runConfirm = true;
  }

  async function onRetractRun(): Promise<void> {
    if (runBusy || runId == null) return;
    runBusy = true;
    runFailed = undefined;
    try {
      const batch = await handleRetractRun(runId);
      runConfirm = false;
      const effects = batch.effect_ids ?? [];
      toast(
        batchText(batch, (count) => `Retracted ${plural(count, 'record')} from this run. Kept in history.`),
        {
          action:
            effects.length > 0
              ? { label: 'Undo', run: () => void handleUndo(effects, 'Restored the records from this run.') }
              : undefined,
        },
      );
      await refreshKnowledge(client);
    } catch (error) {
      runFailed = failure(error);
    } finally {
      runBusy = false;
    }
  }

  function onLink(): void {
    linking = true;
  }

  function onRetry(): void {
    void page.refetch();
    void links.refetch();
    void history.refetch();
  }
</script>

{#snippet details()}
  {#if data && subject}
    <dl class="kmeta">
      <div>
        <dt>Kind</dt>
        <dd>{KINDS[data.kind] ?? data.kind}</dd>
      </div>
      <div>
        <dt>Scope</dt>
        <dd>{subject.scope === 'work' ? 'Work' : 'Personal'}{subject.private ? ', private' : ''}</dd>
      </div>
      <div>
        <dt>Written by</dt>
        <dd>{writerLabel(subject.writer)}</dd>
      </div>
      <div>
        <dt>Created</dt>
        <dd>{formatDayTime(data.created_at)}</dd>
      </div>
      <div>
        <dt>Edited</dt>
        <dd>{formatDayTime(subject.updated_at)}</dd>
      </div>
      <div>
        <dt>Facts</dt>
        <dd>{data.facts.length}{data.facts_truncated ? ' or more' : ''}</dd>
      </div>
      <div>
        <dt>Links</dt>
        <dd>{outgoing.length + incoming.length}</dd>
      </div>
      {#if subject.source}
        <div>
          <dt>Came from</dt>
          <dd>
            {#if subject.source.thread_id}
              <a class="kmeta__link" href={p('/chat/:id', { params: { id: String(subject.source.thread_id) } })}
                >A chat</a
              >
            {:else if subject.source.notion_id}
              Notion
            {:else}
              {subject.source.origin ?? 'atrium'}
            {/if}
          </dd>
        </div>
      {/if}
    </dl>
  {/if}
{/snippet}

{#snippet linkRows(
    list: KnowledgeLink[],
    side: 'target' | 'source',
    label: string,
  )}
  <List {label}>
    {#each list as link (link.id)}
      <ListRow
        title={link[side].title || 'Untitled'}
        meta={linkMeta(link, link[side])}
        href={endHref(link[side])}
        pending={removing === link.id}
      >
        {#snippet end()}
          <Button
            variant="quiet"
            size="sm"
            icon="close"
            label="Remove the link to {link[side].title || 'Untitled'}"
            pending={removing === link.id}
            onclick={() => handleRemove(link, link[side])}
          />
        {/snippet}
      </ListRow>
    {/each}
  </List>
{/snippet}

{#if !data}
  <h1 class="visually-hidden">Knowledge</h1>
{/if}
{#if offline}
  <ViewState kind="offline" title="Cannot reach atrium" level={2}>
    This page loads when the connection returns.
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else if page.isPending}
  <div class="kpage-loading"><ViewState kind="loading" title="Loading the page" /></div>
{:else if missing}
  <ViewState kind="empty" title="This page does not exist" level={2}>
    It may have been deleted. Pick another subject.
  </ViewState>
{:else if page.isError || !data || !subject}
  <ViewState kind="error" title="This page could not be loaded" level={2}>
    {page.error?.message}
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else}
  <article class="kpage" data-retracted={subject.retracted || undefined}>
    <div class="kpage__main">
      <header class="kpage__head">
        <p class="eyebrow">{KINDS[data.kind] ?? data.kind}</p>
        <h1 class="page-title">{subject.title || 'Untitled'}</h1>
        <div class="kpage__tags">
          <Badge tone={stateTag(subject).tone}>{stateTag(subject).label}</Badge>
          {#if subject.pinned}
            <Badge tone="accent">Pinned</Badge>
          {/if}
          {#if subject.private}
            <Badge>Private</Badge>
          {/if}
        </div>
        {#if subject.aliases.length > 0}
          <p class="kpage__aliases">Also known as {subject.aliases.join(', ')}</p>
        {/if}
        {#if !editing}
          <div class="kpage__actions">
            {#if !subject.retracted}
              <Button icon="tool" onclick={onEdit}>Edit</Button>
              {#if !subject.confirmed}
                <Button variant="primary" icon="check" pending={busy === 'confirm'} onclick={handleConfirmPage}
                  >Confirm</Button
                >
              {/if}
              <Button variant="quiet" pending={busy === 'retract'} onclick={handleRetractPage}>Retract</Button>
            {/if}
            <Button variant="quiet" icon="knowledge" aria-keyshortcuts="M" bind:element={mapButton} onclick={onMap}
              >Map
              {#if !phone}
                &nbsp;<kbd class="kpage__kbd">M</kbd>
              {/if}</Button
            >
            {#if saveState === 'saved' && savedAt}
              <p class="kpage__saved" role="status">Saved at {savedAt}</p>
            {/if}
          </div>
        {/if}
      </header>

      {#if phone}
        <details class="kpage__details">
          <summary>Details</summary>
          {@render details()}
        </details>
      {/if}

      {#if subject.retracted}
        <Notice tone="warn" title="Retracted">
          The assistant no longer uses this page. It is kept in history.
          {#snippet action()}
            <Button pending={busy === 'restore'} onclick={handleRestorePage}>Restore</Button>
            <Button variant="danger" onclick={onAskPurge}>Delete for good</Button>
          {/snippet}
        </Notice>
      {/if}

      {#if editing}
        <form class="kpage__editor" aria-label="Edit {subject.title || 'this page'}" onsubmit={handleSave}>
          {#if staleNote}
            <Notice tone="warn" variant="inline" title="This page changed since you opened it">
              It has been reloaded. Your text is still here. Save again to replace the newer version, or cancel to keep
              it.
            </Notice>
          {/if}
          <TextField
            label="Title"
            bind:value={draftTitle}
            required
            error={saveError === 'Give the page a title.' ? saveError : undefined}
          />
          <TextArea
            label="Text"
            help="Markdown works: headings, lists, bold and links."
            bind:value={draftBody}
            rows={14}
            status={saveState === 'saving' ? 'saving' : undefined}
          />
          {#if saveError && saveError !== 'Give the page a title.'}
            <Notice tone="error" variant="inline">{saveError}</Notice>
          {/if}
          <div class="kpage__actions">
            <Button type="submit" variant="primary" pending={saveState === 'saving'}>Save</Button>
            <Button variant="quiet" disabled={saveState === 'saving'} onclick={onCancel}>Cancel</Button>
          </div>
        </form>
      {:else if subject.body.trim()}
        <div class="kpage__body prose"><Markdown text={subject.body} /></div>
      {:else}
        <p class="kpage__blank">This page has no text yet.</p>
      {/if}

      <Section title="Facts" meta={data.facts.length > 0 ? plural(data.facts.length, 'fact') : undefined}>
        {#if data.facts.length === 0}
          <p class="kpage__blank">No facts about this subject yet.</p>
        {:else}
          <List label="Facts">
            {#each data.facts as fact (fact.id)}
              <ListRow
                title={fact.title}
                meta={factMeta(fact)}
                href={p('/knowledge/:id', { params: { id: String(fact.id) } })}
              >
                {#snippet end()}
                  {#if !fact.confirmed || fact.superseded}
                    <Badge tone={stateTag(fact).tone}>{stateTag(fact).label}</Badge>
                  {/if}
                {/snippet}
              </ListRow>
            {/each}
          </List>
          {#if data.facts_truncated}
            <p class="kpage__note">Showing the first {data.facts.length} facts.</p>
          {/if}
        {/if}
      </Section>

      <Section title="Links">
        {#snippet actions()}
          <Button
            size="sm"
            icon="plus"
            onclick={onLink}
            disabled={subject.retracted}
            reason="Restore the page to link it."
          >
            Add a link
          </Button>
        {/snippet}
        {#if links.isPending}
          <div class="kpage-links-loading"><ViewState kind="loading" title="Loading links" /></div>
        {:else if links.isError}
          <ViewState kind="error" title="Links could not be loaded" level={3}>
            {links.error.message}
            {#snippet action()}
              <Button onclick={onRetry}>Try again</Button>
            {/snippet}
          </ViewState>
        {:else}
          <div class="klinks">
            <h3 class="klinks__title">Links from this page</h3>
            {#if outgoing.length === 0}
              <p class="kpage__blank">This page links to nothing yet.</p>
            {:else}
              {@render linkRows(outgoing, 'target', 'Links from this page')}
            {/if}
            <h3 class="klinks__title">What links here</h3>
            {#if incoming.length === 0}
              <p class="kpage__blank">Nothing links here yet.</p>
            {:else}
              {@render linkRows(incoming, 'source', 'What links here')}
            {/if}
          </div>
        {/if}
      </Section>

      {#if runId != null && sameRun.length > 0 && !subject.retracted}
        <Section title="From the same run" meta={plural(sameRun.length, 'other record')}>
          {#snippet actions()}
            <Button size="sm" variant="quiet" onclick={onAskRetractRun}>Retract the run</Button>
          {/snippet}
          <List label="From the same run">
            {#each sameRun as other (other.id)}
              <ListRow
                title={other.title}
                meta="Written by {writerLabel(other.writer)} · edited {formatDay(other.updated_at)}"
                href={p('/knowledge/:id', { params: { id: String(other.id) } })}
              >
                {#snippet end()}
                  {#if !other.confirmed}
                    <Badge tone="warn">Unconfirmed</Badge>
                  {/if}
                {/snippet}
              </ListRow>
            {/each}
          </List>
        </Section>
      {/if}

      <Section title="History">
        {#if history.isPending}
          <div class="kpage-links-loading"><ViewState kind="loading" title="Loading history" /></div>
        {:else if history.isError}
          <ViewState kind="error" title="History could not be loaded" level={3}>
            {history.error.message}
            {#snippet action()}
              <Button onclick={onRetry}>Try again</Button>
            {/snippet}
          </ViewState>
        {:else if (history.data?.history.length ?? 0) === 0}
          <p class="kpage__blank">No changes since it was written.</p>
        {:else}
          <ol class="khistory">
            {#each history.data?.history ?? [] as entry (entry.id)}
              <li class="khistory__item">
                <p class="khistory__line">
                  <span>{historyLabel(entry)}</span>
                  <time datetime={entry.at}>{formatDayTime(entry.at)}</time>
                  {#if entry.undone_at}
                    <Badge>Undone</Badge>
                  {/if}
                </p>
                {#if entry.previous_body}
                  <details class="khistory__prev">
                    <summary>Text before this change</summary>
                    <p class="khistory__body">{entry.previous_body}</p>
                  </details>
                {/if}
              </li>
            {/each}
          </ol>
        {/if}
      </Section>
    </div>
    {#if !phone}
      <aside class="kpage__margin" aria-label="Details">
        {@render details()}
      </aside>
    {/if}
  </article>
  <LinkDialog bind:open={linking} {id} title={subject.title} {phone} />
  <Dialog
    bind:open={purging}
    sheet={phone}
    title="Delete {subject.title || 'this page'} for good?"
    pending={purgeBusy}
    initialFocus="cancel"
  >
    <p>This removes the page, its links and its history from atrium. It cannot be undone.</p>
    {#if purgeFailed}
      <Notice tone="error" variant="inline">{purgeFailed}</Notice>
    {/if}
    {#snippet actions()}
      <Button data-cancel disabled={purgeBusy} onclick={() => (purging = false)}>Cancel</Button>
      <Button variant="danger" pending={purgeBusy} onclick={onPurge}>Delete for good</Button>
    {/snippet}
  </Dialog>
  <Dialog
    bind:open={runConfirm}
    sheet={phone}
    title="Retract {runCount} records from this run?"
    pending={runBusy}
    initialFocus="cancel"
  >
    <p>
      This retracts {subject.title || 'this page'} and {plural(sameRun.length, 'other record')} the same run wrote. The
      assistant stops using them. They stay in history, and you can undo it.
    </p>
    {#if runFailed}
      <Notice tone="error" variant="inline">{runFailed}</Notice>
    {/if}
    {#snippet actions()}
      <Button data-cancel disabled={runBusy} onclick={() => (runConfirm = false)}>Cancel</Button>
      <Button variant="danger" pending={runBusy} onclick={onRetractRun}>Retract {runCount} records</Button>
    {/snippet}
  </Dialog>
{/if}
