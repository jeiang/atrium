<script lang="ts" module>
  let poppedAt = Number.NEGATIVE_INFINITY;
  addEventListener('popstate', () => {
    poppedAt = performance.now();
  });
</script>

<script lang="ts">
  import { createInfiniteQuery } from '@tanstack/svelte-query';
  import { tick, untrack } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import {
    ApiError,
    assistantMessageEdit,
    assistantMessageSend,
    assistantReplyRegenerate,
    assistantRunAnswer,
    assistantRunResume,
    assistantRunStop,
    assistantThreadBranch,
    assistantThreadOptions,
    assistantThreadStart,
    assistantThreadTranscript,
    ok,
    type Step,
    type TranscriptItem,
  } from '../../contract';
  import Composer from '../../shared/assistant/Composer.svelte';
  import StatusSlot from '../../shared/assistant/StatusSlot.svelte';
  import ToolCard from '../../shared/assistant/ToolCard.svelte';
  import type { ToolCall } from '../../shared/assistant/types';
  import { refusedOffline, unreachable } from '../../shared/device.svelte';
  import Markdown from '../../shared/markdown/Markdown.svelte';
  import { queryClient } from '../../shared/query';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p } from '../../shell/router';
  import { useConfiguration } from '../assistant/assistant';
  import ChatActions from './ChatActions.svelte';
  import { failureText } from './manage';
  import ParkedApproval from './ParkedApproval.svelte';
  import ScopeMenu from './ScopeMenu.svelte';
  import { RunStream, type StreamStatus } from './stream.svelte';
  import WorkPanel from './WorkPanel.svelte';
  import './chats.css';

  let { id }: { id: number | undefined } = $props();

  const PAGE = 50;
  const FOLLOW_SLACK = 80;
  const invalid = $derived(id !== undefined && !(Number.isSafeInteger(id) && id > 0));
  const storageKey = $derived(`atrium.chat.scroll.${id ?? 'new'}`);

  const transcript = createInfiniteQuery(() => ({
    queryKey: ['thread', id],
    queryFn: async ({ pageParam, signal }) =>
      ok(await assistantThreadTranscript(id ?? 0, { limit: PAGE, cursor: pageParam || undefined }, { signal })),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled: id !== undefined && !invalid,
    meta: { topics: ['assistant'] },
  }));

  const options = useConfiguration();

  const streams = new SvelteMap<number, RunStream>();
  let root = $state<HTMLElement>();
  let dock = $state<HTMLElement>();
  let draft = $state('');
  let following = $state(true);
  let unseen = $state(false);
  let stopping = $state(false);
  let staged = $state<{ model: string; effort: string }>();
  let optimistic = $state<{ key: number; text: string; after: number }[]>([]);
  let restored = false;
  let sentCount = 0;
  let scrollFrame = 0;
  let phone = $state(false);
  let working = $state(false);
  let editing = $state(false);
  let editText = $state('');
  let editPending = $state(false);
  let editError = $state('');
  let regenerating = $state(false);
  let branching = $state<number>();

  const first = $derived(transcript.data?.pages[0]);
  const items = $derived(
    transcript.data?.pages
      .slice()
      .reverse()
      .flatMap((page) => page.items) ?? [],
  );
  const lastSeq = $derived(items.at(-1)?.seq ?? 0);
  const keys = $derived.by(() => {
    const seen = new Map<number, number>();
    return new Map(
      items.map((entry) => {
        const n = seen.get(entry.seq) ?? 0;
        seen.set(entry.seq, n + 1);
        return [entry, `${entry.seq}:${n}`];
      }),
    );
  });
  const activeRun = $derived(first?.active_run ?? undefined);
  const shown = $derived(
    [...streams.values()]
      .filter(
        (stream) =>
          stream.active ||
          activeRun?.id === stream.run ||
          !items.some((entry) => entry.run_id === stream.run && entry.kind !== 'user'),
      )
      .sort((a, b) => a.run - b.run),
  );
  const liveRuns = $derived(new Set(shown.map((stream) => stream.run)));
  const visible = $derived(
    items.filter((entry) => entry.kind === 'user' || entry.run_id == null || !liveRuns.has(entry.run_id)),
  );
  const userTexts = $derived(new Set(visible.filter((entry) => entry.kind === 'user').map((entry) => entry.text)));
  const queued = $derived(first?.queued ?? []);
  const question = $derived(first?.question ?? undefined);
  const waiting = $derived(
    optimistic.filter(
      (entry) =>
        !items.some((item) => item.kind === 'user' && item.seq > entry.after && item.text === entry.text) &&
        !queued.some((item) => item.text === entry.text),
    ),
  );
  const running = $derived(shown.some((stream) => stream.active) || activeRun?.state === 'running');
  const interrupted = $derived(
    activeRun?.state === 'interrupted' && !streams.get(activeRun.id)?.active ? activeRun : undefined,
  );
  let resuming = $state(false);
  let resumeProblem = $state<{ tone: 'warn' | 'error'; title: string; text: string }>();
  const thread = $derived(first?.thread);
  const model = $derived(staged?.model ?? thread?.model ?? options.data?.defaults.model ?? '');
  const effort = $derived(staged?.effort ?? thread?.effort ?? options.data?.defaults.effort ?? '');
  const title = $derived(id === undefined ? 'New chat' : thread?.title || (thread ? 'Untitled chat' : 'Chat'));
  const ready = $derived(id === undefined || first !== undefined);
  const lastUser = $derived(visible.findLast((entry) => entry.kind === 'user'));
  const lastReply = $derived(
    visible.findLast((entry) => entry.kind === 'assistant' && entry.text && entry.seq > (lastUser?.seq ?? 0)),
  );
  const settledTurn = $derived(
    id !== undefined &&
      !running &&
      activeRun === undefined &&
      !question &&
      waiting.length === 0 &&
      queued.length === 0 &&
      !shown.some((stream) => stream.active),
  );
  const latestRun = $derived(activeRun?.id ?? items.findLast((entry) => entry.run_id != null)?.run_id ?? undefined);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    const onChange = (event: MediaQueryListEvent): void => {
      phone = event.matches;
    };
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  });

  function refresh(): void {
    void queryClient.invalidateQueries({ queryKey: ['thread', id] });
    void queryClient.invalidateQueries({ queryKey: ['threads'] });
  }

  function onEditStart(text: string): void {
    editText = text;
    editError = '';
    editing = true;
  }

  function onEditCancel(): void {
    editing = false;
    editError = '';
  }

  async function onEditSubmit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (id === undefined || editPending) return;
    const text = editText.trim();
    if (!text) {
      editError = 'Write a message first.';
      return;
    }
    editPending = true;
    editError = '';
    try {
      const started = ok(await assistantMessageEdit(id, { text }));
      editing = false;
      following = true;
      follow(started.run_id, true);
      refresh();
    } catch (error) {
      editError = refusedOffline(error)
        ? 'Cannot reach atrium. Nothing was sent. The edit was kept.'
        : failureText(error);
    } finally {
      editPending = false;
    }
  }

  async function onRegenerate(): Promise<void> {
    if (id === undefined || regenerating) return;
    regenerating = true;
    try {
      const started = ok(await assistantReplyRegenerate(id));
      following = true;
      if (started.run_id) follow(started.run_id, true);
      refresh();
    } catch (error) {
      toast(refusedOffline(error) ? 'Cannot reach atrium. No new run was started.' : failureText(error), {
        tone: 'error',
      });
    } finally {
      regenerating = false;
    }
  }

  async function onBranch(seq: number): Promise<void> {
    if (id === undefined || branching !== undefined) return;
    branching = seq;
    try {
      const made = ok(await assistantThreadBranch(id, { seq }));
      void queryClient.invalidateQueries({ queryKey: ['threads'] });
      toast(`Branched “${title}” into a new chat.`);
      await navigate('/chat/:id', { params: { id: String(made.thread_id) } });
    } catch (error) {
      toast(refusedOffline(error) ? 'Cannot reach atrium. No branch was made.' : failureText(error), {
        tone: 'error',
      });
    } finally {
      branching = undefined;
    }
  }

  function onWork(): void {
    working = true;
  }

  function atBottom(): boolean {
    const element = document.documentElement;
    return element.scrollHeight - (window.scrollY + window.innerHeight) <= FOLLOW_SLACK;
  }

  let lastY = 0;

  function toBottom(): void {
    const top = document.documentElement.scrollHeight - window.innerHeight;
    if (Math.abs(window.scrollY - top) > 1) window.scrollTo({ top, behavior: 'instant' });
    lastY = window.scrollY;
  }

  function keepFollowing(): void {
    if (!following) return;
    if (window.scrollY < lastY && !atBottom()) {
      following = false;
      return;
    }
    toBottom();
  }

  function onApplied(): void {
    keepFollowing();
    if (!following) unseen = true;
  }

  function onStreamEnd(status: StreamStatus, run: number): void {
    if (status === 'expired') streams.delete(run);
    void queryClient.invalidateQueries({ queryKey: ['thread', id] });
    void queryClient.invalidateQueries({ queryKey: ['threads'] });
  }

  function follow(run: number, resume: boolean): void {
    if (id === undefined) return;
    const existing = streams.get(run);
    if (existing) {
      if (resume && existing.resumable) existing.start();
      return;
    }
    const stream = new RunStream(id, run, {
      onapply: onApplied,
      onend: (status) => onStreamEnd(status, run),
    });
    streams.set(run, stream);
    stream.start();
  }

  $effect(() => {
    const run = activeRun;
    if (!run) return;
    const resume = run.state === 'running' || run.state === 'interrupted';
    untrack(() => follow(run.id, resume));
  });

  $effect(() => () => {
    for (const stream of streams.values()) stream.dispose();
  });

  $effect(() => {
    lastY = window.scrollY;
    let touchY = 0;
    // True when an element under the input can still move that way itself, so the page will not scroll.
    const inner = (target: EventTarget | null, up: boolean): boolean => {
      for (
        let node = target instanceof Element ? target : null;
        node && node !== document.body;
        node = node.parentElement
      ) {
        const overflow = getComputedStyle(node).overflowY;
        if (overflow !== 'auto' && overflow !== 'scroll') continue;
        if (up ? node.scrollTop > 0 : node.scrollTop + node.clientHeight < node.scrollHeight) return true;
      }
      return false;
    };
    // Upward operator input stops following before its first scroll step, which can land inside the slack.
    const scrolledUp = (target: EventTarget | null): void => {
      if (window.scrollY === 0 || dock?.contains(target as Node) || inner(target, true)) return;
      following = false;
    };
    const onWheel = (event: WheelEvent): void => {
      if (event.deltaY < 0) scrolledUp(event.target);
    };
    const onTouchStart = (event: TouchEvent): void => {
      touchY = event.touches[0]?.clientY ?? 0;
    };
    const onTouchMove = (event: TouchEvent): void => {
      const y = event.touches[0]?.clientY ?? touchY;
      if (y > touchY) scrolledUp(event.target);
      touchY = y;
    };
    const onKey = (event: KeyboardEvent): void => {
      const target = event.target;
      if (target instanceof HTMLElement && (target.isContentEditable || target.closest('input, textarea, select')))
        return;
      if (['ArrowUp', 'PageUp', 'Home'].includes(event.key) || (event.key === ' ' && event.shiftKey))
        scrolledUp(target);
    };
    const onScroll = (): void => {
      const y = window.scrollY;
      const bottom = atBottom();
      if (bottom && y > lastY) following = true;
      else if (!bottom && y < lastY) following = false;
      lastY = y;
      if (following) unseen = false;
      if (scrollFrame) return;
      scrollFrame = requestAnimationFrame(() => {
        scrollFrame = 0;
        sessionStorage.setItem(storageKey, String(Math.round(window.scrollY)));
      });
    };
    addEventListener('touchstart', onTouchStart, { passive: true });
    addEventListener('touchmove', onTouchMove, { passive: true });
    addEventListener('keydown', onKey);
    addEventListener('scroll', onScroll, { passive: true });
    addEventListener('wheel', onWheel, { passive: true });
    return () => {
      removeEventListener('wheel', onWheel);
      removeEventListener('touchstart', onTouchStart);
      removeEventListener('touchmove', onTouchMove);
      removeEventListener('keydown', onKey);
      removeEventListener('scroll', onScroll);
      cancelAnimationFrame(scrollFrame);
    };
  });

  $effect(() => {
    if (!first || restored) return;
    restored = true;
    const saved = Number(sessionStorage.getItem(storageKey));
    const back = performance.now() - poppedAt < 1500 && saved > 0;
    following = !back;
    void tick().then(() => {
      if (back) {
        window.scrollTo({ top: saved, behavior: 'instant' });
        following = atBottom();
      } else {
        toBottom();
      }
    });
  });

  $effect(() => {
    void visible.length;
    void waiting.length;
    void queued.length;
    if (!restored) return;
    untrack(() => {
      if (following) void tick().then(keepFollowing);
    });
  });

  $effect(() => {
    if (!root) return;
    const observer = new ResizeObserver(() => {
      if (restored) keepFollowing();
    });
    observer.observe(root);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!dock || !root) return;
    const target = root;
    let frame = 0;
    const observer = new ResizeObserver(([entry]) => {
      const height = Math.ceil(entry?.borderBoxSize[0]?.blockSize ?? 0);
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => target.style.setProperty('--chat-dock', `${height}px`));
    });
    observer.observe(dock);
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  });

  function toolCalls(steps: Step[] | null): ToolCall[] {
    return (steps ?? [])
      .filter((step) => step.kind !== 'note' && step.kind !== 'thinking' && step.id)
      .map((step) => ({
        id: step.id ?? step.name,
        name: step.name,
        args: step.args,
        state: step.is_error ? 'failed' : step.state === 'running' ? 'running' : 'done',
        preview: step.error ?? step.result ?? undefined,
        results: step.results.map((result) => ({ title: result.title || result.url, url: result.url })),
      }));
  }

  function proposalFor(run: number): TranscriptItem | undefined {
    return items.findLast((entry) => entry.kind === 'proposal' && entry.run_id === run && entry.proposal_id);
  }

  function statusOf(stream: RunStream): { text: string; tone: 'neutral' | 'warn' | 'error' | 'accent' } | undefined {
    if (stream.status === 'busy') return { text: 'Too many chats are streaming. Trying again soon', tone: 'warn' };
    if (stream.status === 'retrying') return { text: 'Connection lost. Reconnecting', tone: 'warn' };
    if (stream.status === 'failed') return { text: stream.error ?? 'The run failed', tone: 'error' };
    if (stream.status === 'stopped') return { text: 'Stopped. Steps already done stay done', tone: 'neutral' };
    if (stream.status === 'parked') return { text: 'Waiting for you', tone: 'warn' };
    if (stream.active && stream.segments.length === 0) return { text: 'Starting', tone: 'accent' };
    return undefined;
  }

  function message(error: unknown): string {
    if (error instanceof TypeError || refusedOffline(error))
      return 'Cannot reach atrium. Nothing was sent. The message was kept.';
    return error instanceof Error && error.message ? error.message : 'The message was not sent.';
  }

  async function onSend(text: string): Promise<void> {
    if (id === undefined) {
      try {
        const started = ok(
          await assistantThreadStart({ text, model: model || undefined, effort: effort || undefined }),
        );
        await navigate('/chat/:id', { params: { id: String(started.thread.id) }, replace: true });
      } catch (error) {
        throw new Error(message(error));
      }
      return;
    }
    const key = ++sentCount;
    if (question) {
      optimistic.push({ key, text, after: lastSeq });
      following = true;
      try {
        const started = ok(await assistantRunAnswer(id, { text }));
        follow(started.run_id, true);
        void queryClient.invalidateQueries({ queryKey: ['thread', id] });
        void queryClient.invalidateQueries({ queryKey: ['threads'] });
      } catch (error) {
        optimistic = optimistic.filter((entry) => entry.key !== key);
        throw new Error(message(error));
      }
      return;
    }
    try {
      if (staged) {
        ok(await assistantThreadOptions(id, staged));
        staged = undefined;
      }
      optimistic.push({ key, text, after: lastSeq });
      following = true;
      const sent = ok(await assistantMessageSend(id, { text }));
      if (sent.run_id) follow(sent.run_id, true);
      void queryClient.invalidateQueries({ queryKey: ['thread', id] });
      void queryClient.invalidateQueries({ queryKey: ['threads'] });
    } catch (error) {
      optimistic = optimistic.filter((entry) => entry.key !== key);
      throw new Error(message(error));
    }
  }

  async function onStop(): Promise<void> {
    if (id === undefined) return;
    stopping = true;
    try {
      const result = ok(await assistantRunStop(id));
      streams.get(result.run_id ?? -1)?.close('stopped');
      for (const stream of streams.values()) if (stream.active) stream.close('stopped');
      toast('Stopped. Steps already done stay done.');
      void queryClient.invalidateQueries({ queryKey: ['thread', id] });
    } catch (error) {
      toast(error instanceof ApiError ? error.message : 'The run could not be stopped.', { tone: 'error' });
    } finally {
      stopping = false;
    }
  }

  function resumeRefused(error: unknown): { tone: 'warn' | 'error'; title: string; text: string } {
    if (refusedOffline(error) && error instanceof Error)
      return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
    if (error instanceof ApiError)
      return { tone: 'warn', title: 'atrium refused this', text: `${error.message} Nothing was resumed.` };
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered, so the run may or may not have resumed. atrium reloaded the chat and did not try again.',
    };
  }

  async function onResume(): Promise<void> {
    const run = interrupted;
    if (id === undefined || !run || resuming) return;
    resuming = true;
    resumeProblem = undefined;
    try {
      ok(await assistantRunResume(id, run.id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
      following = true;
      follow(run.id, true);
    } catch (error) {
      resumeProblem = resumeRefused(error);
    } finally {
      resuming = false;
      void queryClient.invalidateQueries({ queryKey: ['thread', id] });
      void queryClient.invalidateQueries({ queryKey: ['threads'] });
    }
  }

  function onScope(nextModel: string, nextEffort: string): void {
    const sameAsThread = nextModel === thread?.model && nextEffort === thread?.effort;
    staged = sameAsThread ? undefined : { model: nextModel, effort: nextEffort };
  }

  function onJump(): void {
    following = true;
    unseen = false;
    toBottom();
  }

  async function onEarlier(): Promise<void> {
    const before = document.documentElement.scrollHeight;
    const top = window.scrollY;
    await transcript.fetchNextPage();
    await tick();
    window.scrollTo({ top: top + document.documentElement.scrollHeight - before, behavior: 'instant' });
  }

  function onDecided(): void {
    void queryClient.invalidateQueries({ queryKey: ['thread', id] });
  }

  function onRetry(): void {
    void transcript.refetch();
  }
</script>

{#snippet bubble(
    text: string,
    note?: string,
    editable?: boolean,
  )}
  <li class="turn turn--you">
    <div class="bubble">
      <p class="bubble__text">{text}</p>
      {#if note}
        <span class="bubble__note">{note}</span>
      {/if}
    </div>
    {#if editable}
      <div class="turn__actions">
        <Button size="sm" variant="quiet" onclick={() => onEditStart(text)}>Edit</Button>
      </div>
    {/if}
  </li>
{/snippet}

{#snippet tools(
    calls: ToolCall[],
    run?: number,
  )}
  <div class="tools">
    {#each calls as call, index (`${index}:${call.id}`)}
      <ToolCard {call}>
        {#snippet approval()}
          {const ask = $derived(run === undefined ? undefined : proposalFor(run))}
          {#if ask?.proposal_id && run !== undefined && calls.find((entry) => entry.state === 'waiting') === call}
            <ParkedApproval id={ask.proposal_id} {run} thread={title} ondecided={onDecided} />
          {/if}
        {/snippet}
      </ToolCard>
    {/each}
  </div>
{/snippet}

<div class="chat" bind:this={root}>
  <header class="chat__head">
    <a class="chat__back link" href={p('/chat')}><Icon name="chevron" />All chats</a>
    <div class="chat__heading">
      <h1 class="chat__title" tabindex="-1">{title}</h1>
      {#if thread && id !== undefined}
        <ChatActions {thread} {running} {phone} onwork={onWork} />
      {/if}
    </div>
    {#if thread?.archived || thread?.branched_from_thread_id}
      <p class="chat__origin">
        {#if thread.archived}
          <span>Archived</span>
        {/if}
        {#if thread.branched_from_thread_id}
          <a class="link" href={p('/chat/:id', { params: { id: String(thread.branched_from_thread_id) } })}
            >Branched from an earlier chat</a
          >
        {/if}
      </p>
    {/if}
  </header>

  {#if invalid}
    <ViewState kind="error" title="This chat does not exist" level={2}>
      Check the link, or open a chat from the list.
    </ViewState>
  {:else if id !== undefined && unreachable(transcript) && !first}
    <ViewState kind="offline" title="Cannot reach atrium" level={2}>
      The chat loads again when the connection returns.
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if id !== undefined && transcript.isPending}
    <div class="chat__skeleton">
      <ViewState kind="loading" title="Loading the chat" />
    </div>
  {:else if id !== undefined && transcript.isError && !first}
    <ViewState kind="error" title="The chat could not be loaded" level={2}>
      {transcript.error.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else}
    {#if transcript.hasNextPage}
      <div class="chat__earlier">
        <Button size="sm" pending={transcript.isFetchingNextPage} onclick={onEarlier}>Show earlier messages</Button>
      </div>
    {/if}
    {#if visible.length === 0 && shown.length === 0 && waiting.length === 0 && queued.length === 0}
      <ViewState kind="empty" title={id === undefined ? 'Start a chat' : 'Nothing here yet'} level={2}>
        Ask for something below. Only Send starts work.
      </ViewState>
    {/if}
    <ol class="transcript" aria-label="Messages">
      {#each visible as entry (keys.get(entry))}
        {#if entry.kind === 'user' && entry === lastUser && settledTurn && editing}
          <li class="turn turn--you">
            <form class="chat-edit" onsubmit={onEditSubmit}>
              <TextArea label="Edit your last message" rows={3} bind:value={editText} disabled={editPending} />
              {#if editError}
                <Notice tone="error" variant="inline">{editError}</Notice>
              {/if}
              <div class="turn__actions">
                <Button size="sm" disabled={editPending} onclick={onEditCancel}>Cancel</Button>
                <Button size="sm" variant="primary" type="submit" pending={editPending}>Send edit (new run)</Button>
              </div>
            </form>
          </li>
        {:else if entry.kind === 'user'}
          {@render bubble(entry.text, undefined, entry === lastUser && settledTurn)}
        {:else if entry.kind === 'activity'}
          {const calls = $derived(toolCalls(entry.steps))}
          {#if calls.length > 0}
            <li class="turn">{@render tools(calls)}</li>
          {/if}
        {:else if entry.kind === 'proposal' && entry.proposal_id && entry.run_id != null}
          <li class="turn">
            <ParkedApproval id={entry.proposal_id} run={entry.run_id} thread={title} ondecided={onDecided} />
          </li>
        {:else if entry.kind === 'compaction'}
          <li class="turn turn--divider">Earlier messages were summarized</li>
        {:else if entry.text}
          <li class="turn">
            {#if entry.kind === 'draft'}
              <span class="eyebrow">Draft</span>
            {/if}
            <Markdown text={entry.text} />
            {#if entry.kind === 'assistant' && settledTurn}
              <div class="turn__actions">
                {#if entry === lastReply}
                  <Button size="sm" variant="quiet" pending={regenerating} onclick={onRegenerate}>
                    Regenerate (new run)
                  </Button>
                {/if}
                <Button size="sm" variant="quiet" pending={branching === entry.seq} onclick={() => onBranch(entry.seq)}>
                  Branch from here
                </Button>
              </div>
            {/if}
          </li>
        {/if}
      {/each}
      {#each shown as stream (stream.run)}
        {#each stream.segments as segment, index (index)}
          {#if segment.kind === 'user'}
            {#if !userTexts.has(segment.text)}
              {@render bubble(segment.text)}
            {/if}
          {:else if segment.kind === 'text'}
            <li class="turn"><Markdown text={segment.text} /></li>
          {:else}
            {const calls = $derived(stream.calls.filter((call) => segment.ids.includes(call.id)))}
            <li class="turn">{@render tools(calls, stream.run)}</li>
          {/if}
        {/each}
        {const status = $derived(statusOf(stream))}
        {#if status}
          <li class="turn turn--status"><StatusSlot text={status.text} tone={status.tone} /></li>
        {/if}
      {/each}
      {#if question}
        <li class="turn turn--question">
          <span class="eyebrow">Question for you</span>
          <Markdown text={question.text} />
        </li>
      {/if}
      {#if interrupted || resumeProblem}
        <li class="turn turn--resume">
          {#if interrupted}
            <Notice tone="warn" variant="inline" title="This run was interrupted">
              It stopped while a step was in flight. Resuming lets the assistant check what happened before it
              continues.
              {#snippet action()}
                <Button variant="secondary" pending={resuming} onclick={onResume}>Resume run</Button>
              {/snippet}
            </Notice>
          {/if}
          {#if resumeProblem}
            <Notice tone={resumeProblem.tone} variant="inline" title={resumeProblem.title}>{resumeProblem.text}</Notice>
          {/if}
        </li>
      {/if}
      {#each queued as entry (entry.id)}
        {@render bubble(entry.text, 'Queued')}
      {/each}
      {#each waiting as entry (entry.key)}
        {@render bubble(entry.text, 'Sending')}
      {/each}
    </ol>
  {/if}

  <div class="chat__dock" bind:this={dock}>
    {#if !following && (unseen || running)}
      <button type="button" class="chat__jump" onclick={onJump}><Icon name="chevron" />Jump to latest</button>
    {/if}
    {#if !invalid}
      <Composer
        bind:value={draft}
        label={id === undefined ? 'New chat message' : question ? 'Answer' : 'Reply'}
        placeholder={id === undefined ? 'Ask for something' : question ? 'Answer the question' : 'Reply or ask'}
        disabled={!ready}
        {running}
        {stopping}
        onsubmit={onSend}
        onstop={onStop}
      >
        {#snippet scope()}
          <ScopeMenu options={options.data} {model} {effort} staged={staged !== undefined} onchange={onScope} />
        {/snippet}
      </Composer>
    {/if}
  </div>
  {#if id !== undefined && !invalid}
    <WorkPanel bind:open={working} thread={id} run={latestRun} />
  {/if}
</div>
