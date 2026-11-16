<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { blockNavigation } from 'sv-router';
  import { tick } from 'svelte';
  import { announce } from '../../shared/announce.svelte';
  import { formatClock, formatDayTime, plural } from '../../shared/assistant/format';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import Markdown from '../../shared/markdown/Markdown.svelte';
  import { failureText, handleSaveEdit, isStale, refreshKnowledge, useInstructions } from './knowledge';
  import LineDiff from './LineDiff.svelte';
  import { type Version, versions } from './versions';

  let { phone }: { phone: boolean } = $props();

  const client = useQueryClient();
  const instructions = useInstructions();

  let editing = $state(false);
  let draft = $state('');
  let base = $state('');
  let saved = $state('');
  let restoring = $state<Version>();
  let staleText = $state<string>();
  let saving = $state(false);
  let savedAt = $state<string>();
  let error = $state<string>();
  let pane = $state('write');
  let open = $state<string>();
  let view = $state('text');
  let leaving = $state(false);
  let settle: ((leave: boolean) => void) | undefined;
  let editButton = $state<HTMLButtonElement>();
  let refocus = false;

  const data = $derived(instructions.data);
  const record = $derived(data?.record);
  const list = $derived(record ? versions(record, data?.history ?? []) : []);
  const dirty = $derived(editing && draft !== saved);
  const offline = $derived(instructions.fetchStatus === 'paused' && !data);

  $effect(() =>
    blockNavigation({
      beforeUnload: () => !dirty,
      onNavigate: () => !dirty || ask(),
    }),
  );

  function ask(): Promise<boolean> {
    settle?.(false);
    leaving = true;
    return new Promise((resolve) => {
      settle = resolve;
    });
  }

  function answer(leave: boolean): void {
    const done = settle;
    settle = undefined;
    leaving = false;
    done?.(leave);
  }

  function onKeep(): void {
    answer(false);
  }

  function onDiscard(): void {
    answer(true);
  }

  function onLeaveClosed(): void {
    answer(false);
    // The dialog hands focus back to its opener when it closes; after a discard that opener is gone.
    if (refocus) editButton?.focus();
    refocus = false;
  }

  function who(by: string): string {
    if (by === 'operator') return 'you';
    if (by === 'assistant') return 'the assistant';
    return by;
  }

  function versionName(version: Version): string {
    return version.current ? 'the current text' : `the version from ${formatDayTime(version.at)}`;
  }

  function begin(text: string, from?: Version): void {
    if (!record) return;
    draft = text;
    saved = from ? record.body : text;
    base = record.updated_at;
    restoring = from;
    staleText = undefined;
    error = undefined;
    pane = 'write';
    editing = true;
  }

  function onEdit(): void {
    if (record) begin(record.body);
  }

  function onRestore(version: Version): void {
    begin(version.text, version);
    announce(`The version from ${formatDayTime(version.at)} is in the editor. Save to make it current.`);
  }

  async function onCancel(): Promise<void> {
    const asked = dirty;
    if (asked && !(await ask())) return;
    refocus = asked;
    editing = false;
    restoring = undefined;
    staleText = undefined;
    error = undefined;
    await tick();
    editButton?.focus();
  }

  async function handleSave(event?: SubmitEvent): Promise<void> {
    event?.preventDefault();
    if (saving || !record) return;
    saving = true;
    error = undefined;
    try {
      await handleSaveEdit(record.id, base, { title: record.title, body: draft });
      savedAt = formatClock(new Date());
      editing = false;
      restoring = undefined;
      staleText = undefined;
      announce('Saved. The assistant uses the new text from its next run.');
      await refreshKnowledge(client);
    } catch (failure) {
      if (isStale(failure)) {
        const fresh = await instructions.refetch();
        base = fresh.data?.record.updated_at ?? base;
        saved = fresh.data?.record.body ?? saved;
        staleText = fresh.data?.record.body ?? '';
        announce('The instructions changed since you opened them. They have been reloaded. Your text is kept.');
        return;
      }
      error = failureText(failure, 'You are offline. Your text is kept; save again when the connection returns.');
    } finally {
      saving = false;
    }
  }

  function onEditorKey(event: KeyboardEvent): void {
    if (event.isComposing || event.keyCode === 229) return;
    if ((event.metaKey || event.ctrlKey) && (event.key === 'Enter' || event.key.toLowerCase() === 's')) {
      event.preventDefault();
      void handleSave();
    }
  }

  function onToggle(version: Version): void {
    open = open === version.key ? undefined : version.key;
    view = 'text';
  }

  function onRetry(): void {
    void instructions.refetch();
  }
</script>

{#snippet preview(
    text: string,
  )}
  {#if text.trim()}
    <div class="kpage__body prose"><Markdown {text} /></div>
  {:else}
    <p class="kpage__blank">Nothing written yet.</p>
  {/if}
{/snippet}

{#if offline}
  <h1 class="visually-hidden">Standing instructions</h1>
  <ViewState kind="offline" title="You are offline" level={2}>
    The instructions load when the connection returns.
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else if instructions.isPending}
  <h1 class="visually-hidden">Standing instructions</h1>
  <div class="kpage-loading"><ViewState kind="loading" title="Loading the instructions" /></div>
{:else if instructions.isError || !record}
  <h1 class="visually-hidden">Standing instructions</h1>
  <ViewState kind="error" title="The instructions could not be loaded" level={2}>
    {instructions.error?.message}
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else}
  <article class="kpage kpage--single">
    <div class="kpage__main">
      <header class="kpage__head">
        <p class="eyebrow">Standing instructions</p>
        <h1 class="page-title">{record.title || 'Standing instructions'}</h1>
        <div class="kpage__tags"><Badge tone="accent">Pinned</Badge></div>
        <p class="page-lede">
          The assistant reads this page on every run. Helpers follow fixed instructions that are not edited here.
        </p>
        {#if !editing}
          <div class="kpage__actions">
            <Button icon="tool" bind:element={editButton} onclick={onEdit}>Edit</Button>
            {#if savedAt}
              <p class="kpage__saved" role="status">Saved at {savedAt}</p>
            {/if}
          </div>
        {/if}
      </header>

      {#if editing}
        <form class="kinstr__editor" aria-label="Edit the standing instructions" onsubmit={handleSave}>
          {#if restoring}
            <Notice tone="info" variant="inline" title="Restoring the version from {formatDayTime(restoring.at)}">
              Nothing changes until you save. Saving makes this text current and keeps the present text in history.
            </Notice>
          {/if}
          {#if staleText !== undefined}
            <Notice tone="warn" variant="inline" title="The instructions changed since you opened them">
              They have been reloaded. Your text is still here. Save again to replace the newer version, or cancel to
              keep it.
            </Notice>
            <details class="khistory__prev">
              <summary>Compare your text with the newer version</summary>
              <LineDiff
                from={staleText}
                to={draft}
                label="Differences between the newer version and your text"
                fromName="the newer version"
                toName="your text"
              />
            </details>
          {/if}
          {#if phone}
            <Segmented
              label="Show"
              options={[
    { value: 'write', label: 'Write' },
    { value: 'preview', label: 'Preview' },
  ]}
              bind:value={pane}
            />
          {/if}
          <div class="kinstr__panes">
            <div class="kinstr__pane" hidden={phone && pane !== 'write'}>
              <TextArea
                label="Instructions"
                help="Markdown works. Press {navigator.userAgent.includes('Mac') ? '⌘' : 'Ctrl'} Enter to save."
                bind:value={draft}
                rows={16}
                spellcheck="true"
                status={saving ? 'saving' : undefined}
                onkeydown={onEditorKey}
              />
            </div>
            <section class="kinstr__pane kinstr__preview" aria-label="Preview" hidden={phone && pane !== 'preview'}>
              <h2 class="klinks__title">Preview</h2>
              {@render preview(draft)}
            </section>
          </div>
          {#if error}
            <Notice tone="error" variant="inline">{error}</Notice>
          {/if}
          <div class="kpage__actions">
            <Button type="submit" variant="primary" pending={saving}>Save</Button>
            <Button variant="quiet" disabled={saving} onclick={onCancel}>Cancel</Button>
            {#if dirty}
              <p class="kpage__note" role="status">Not saved yet</p>
            {/if}
          </div>
        </form>
      {:else}
        {@render preview(record.body)}
      {/if}

      <Section title="History" meta={plural(list.length, 'version')}>
        <ol class="kversions" aria-label="Versions">
          {#each list as version (version.key)}
            <li class="kversion" data-open={open === version.key || undefined}>
              <div class="kversion__row">
                <p class="kversion__line">
                  <span>{version.current ? 'Current text' : 'Earlier text'}, saved by {who(version.by)}</span>
                  <time datetime={version.at}>{formatDayTime(version.at)}</time>
                  {#if version.undone}
                    <Badge>Undone</Badge>
                  {/if}
                </p>
                <Button variant="quiet" size="sm" aria-expanded={open === version.key} onclick={() => onToggle(version)}
                  >{open === version.key ? 'Hide' : 'View'}
                  <span class="visually-hidden">{versionName(version)}</span></Button
                >
              </div>
              {#if open === version.key}
                <div class="kversion__body">
                  {#if !version.current}
                    <Segmented
                      label="Show the version from {formatDayTime(version.at)}"
                      options={[
    { value: 'text', label: 'Text' },
    { value: 'compare', label: 'Compare with current' },
  ]}
                      bind:value={view}
                    />
                  {/if}
                  {#if view === 'compare' && !version.current}
                    <LineDiff
                      from={version.text}
                      to={record.body}
                      label="Differences from {versionName(version)} to the current text"
                      fromName="this version"
                      toName="the current text"
                    />
                  {:else}
                    <pre class="kversion__text">{version.text || 'Empty'}</pre>
                  {/if}
                  {#if !version.current}
                    <div class="kpage__actions">
                      <Button
                        icon="undo"
                        disabled={editing}
                        reason="Save or cancel your edit first."
                        onclick={() => onRestore(version)}
                        >Restore this version</Button
                      >
                    </div>
                  {/if}
                </div>
              {/if}
            </li>
          {/each}
        </ol>
      </Section>
    </div>
  </article>
{/if}

<Dialog bind:open={leaving} title="Leave without saving?" initialFocus="cancel" sheet={phone} onclose={onLeaveClosed}>
  <p>Your changes to the standing instructions are not saved. Leaving discards them.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={onKeep}>Keep editing</Button>
    <Button variant="danger" onclick={onDiscard}>Discard changes</Button>
  {/snippet}
</Dialog>
