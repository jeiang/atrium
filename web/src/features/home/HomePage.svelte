<script lang="ts">
  import { createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    ApiError,
    type Approval,
    assistantHome,
    assistantThreadStart,
    type Draft,
    ok,
    reviewPending,
  } from '../../contract';
  import Composer from '../../shared/assistant/Composer.svelte';
  import Digest from '../../shared/assistant/Digest.svelte';
  import { inboxPath } from '../../shared/assistant/inboxId';
  import WeatherCard from '../../shared/assistant/WeatherCard.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useShell } from '../../shell/frame';
  import { moduleOn } from '../../shell/nav';
  import { navigate, p } from '../../shell/router';
  import { ageMinutes, ageText, STALE_MINUTES } from './weather';
  import './home.css';

  const HOME_TOPICS = ['assistant', 'settings', 'calendar', 'tasks', 'attention', 'mail', 'knowledge'] as const;
  const PENDING_TOPICS = [
    'review',
    'effects',
    'mail',
    'calendar',
    'tasks',
    'assistant',
    'settings',
    'connections',
  ] as const;
  const NEEDS_LIMIT = 5;
  const DIGEST_LIMIT = 5;
  const digestRows = Array.from({ length: DIGEST_LIMIT }, (_, index) => index);
  const LOCATION_SETTINGS = '/settings/assistant';
  const base = import.meta.env.BASE_URL.replace(/\/$/, '');

  const client = useQueryClient();
  const shell = useShell();
  const assistantOn = $derived(moduleOn(shell.data, 'assistant'));
  const waiting = $derived(shell.data?.needs_you ?? 0);

  const home = createQuery(() => ({
    queryKey: ['home'],
    queryFn: async ({ signal }) => ok(await assistantHome(undefined, { signal })),
    enabled: assistantOn,
    meta: { topics: HOME_TOPICS },
  }));

  const pending = createQuery(() => ({
    queryKey: ['pending'],
    queryFn: async ({ signal }) => ok(await reviewPending({ signal })),
    meta: { topics: PENDING_TOPICS },
  }));

  interface Need {
    key: string;
    title: string;
    meta: string;
    href: string;
  }

  function approvalNeed(item: Approval, blocked: boolean): Need {
    return {
      key: `proposal:${item.id}`,
      title: item.title,
      meta: blocked ? `${item.kind_label} · blocked` : item.kind_label,
      href: base + inboxPath('proposal', item.id),
    };
  }

  function draftNeed(item: Draft): Need {
    return {
      key: `draft:${item.id}`,
      title: item.subject || `Mail to ${item.to}`,
      meta: item.status === 'unknown' ? `Mail to ${item.to} · outcome unknown` : `Draft mail to ${item.to}`,
      href: base + inboxPath('draft', item.id),
    };
  }

  const needs = $derived.by((): Need[] => {
    const data = pending.data;
    if (!data) return [];
    return [
      ...data.approvals.map((item) => approvalNeed(item, false)),
      ...data.blocked.map((item) => approvalNeed(item, true)),
      ...data.drafts.map(draftNeed),
    ].slice(0, NEEDS_LIMIT);
  });
  const reservedRows = $derived(Array.from({ length: Math.min(waiting, NEEDS_LIMIT) }, (_, index) => index));

  const weather = $derived(home.data?.weather ?? null);
  const age = $derived(weather ? ageMinutes(weather.current.time, weather.timezone, new Date()) : undefined);
  const stale = $derived(age !== undefined && age > STALE_MINUTES ? age : undefined);

  const offline = (query: { isPending: boolean; fetchStatus: string }): boolean =>
    query.isPending && query.fetchStatus === 'paused';

  let draft = $state('');
  let sending = $state(false);
  async function onSubmit(text: string): Promise<void> {
    sending = true;
    try {
      const started = ok(await assistantThreadStart({ text }));
      draft = '';
      void client.invalidateQueries({ queryKey: ['home'] });
      await navigate('/chat/:id', { params: { id: String(started.thread.id) } });
    } catch (error) {
      toast(error instanceof ApiError ? error.message : 'The chat did not start. Try again.', { tone: 'error' });
    } finally {
      sending = false;
    }
  }
</script>

<div class="home">
  <h1 class="visually-hidden">Home</h1>

  {#if assistantOn}
    <div class="home__composer">
      <Composer bind:value={draft} busy={sending} placeholder="Ask atrium anything" onsubmit={onSubmit} />
    </div>
  {:else}
    <div class="home__off">
      <ViewState kind="blocked" title="The assistant is off" level={2}>
        Turn the assistant on in Settings to ask from Home. What waits for you is still listed here.
      </ViewState>
    </div>
  {/if}

  <div class="home__needs">
    <Section title="Needs you" meta={`${waiting} waiting`}>
      {#snippet actions()}
        <a class="link home__all" href={p('/inbox')}>All in Inbox</a>
      {/snippet}
      {#if pending.data}
        {#if needs.length === 0}
          <ViewState kind="empty" title="Nothing needs you">
            When atrium wants to send, spend or change something outside your notes, it waits here.
          </ViewState>
        {:else}
          <List label="Waiting for you">
            {#each needs as need (need.key)}
              <ListRow title={need.title} meta={need.meta} href={need.href} />
            {/each}
          </List>
        {/if}
      {:else if pending.isError}
        <ViewState kind="error" title="The list did not load">
          {pending.error?.message}
          {#snippet action()}
            <Button onclick={() => pending.refetch()}>Try again</Button>
          {/snippet}
        </ViewState>
      {:else if offline(pending)}
        <ViewState kind="offline" title="You are offline">The list appears when the connection is back.</ViewState>
      {:else if reservedRows.length > 0}
        <ul class="list home__skeleton" aria-busy="true">
          <li class="visually-hidden">Loading what needs you</li>
          {#each reservedRows as row (row)}
            <li class="row" aria-hidden="true">
              <span class="row__main">
                <span class="skel home__skel-title"></span>
                <span class="skel home__skel-meta"></span>
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </Section>
  </div>

  {#if assistantOn}
    <div class="home__weather">
      {#if home.data}
        {#if weather}
          <WeatherCard {weather} level={2}>
            {#snippet note()}
              {#if stale !== undefined}
                <p class="weather__plan home__stale">Last updated {ageText(stale)}.</p>
              {/if}
            {/snippet}
          </WeatherCard>
        {:else}
          <WeatherCard weather={null} level={2}>
            {#snippet note()}
              <p class="weather__plan">
                No forecast right now. Set a location to see it here; if one is set, the forecast service did not answer
                and Home tries again on the next refresh.
              </p>
              <a class="link home__setting" href={LOCATION_SETTINGS}>Set a location in the current interface</a>
            {/snippet}
          </WeatherCard>
        {/if}
      {:else if home.isError}
        <ViewState kind="error" title="Home did not load" level={2}>
          {home.error?.message}
          {#snippet action()}
            <Button onclick={() => home.refetch()}>Try again</Button>
          {/snippet}
        </ViewState>
      {:else if offline(home)}
        <ViewState kind="offline" title="You are offline" level={2}>
          The weather and the digest appear when the connection is back.
        </ViewState>
      {:else}
        <section class="card weather home__skeleton" aria-busy="true" aria-label="Loading the weather">
          <span class="skel home__skel-head"></span>
          <span class="skel home__skel-figure"></span>
          <span class="skel home__skel-facts"></span>
          <span class="skel"></span>
        </section>
      {/if}
    </div>

    {#if home.data}
      <div class="home__digest card">
        <Digest
          lines={home.data.digest}
          title="Today's digest"
          limit={DIGEST_LIMIT}
          level={2}
          more={p('/chat')}
          empty="No digest yet today. The morning brief adds it here."
        />
      </div>
    {:else if !home.isError && !offline(home)}
      <div class="home__digest card">
        <section class="digest home__skeleton" aria-busy="true" aria-label="Loading the digest">
          <span class="skel home__skel-head"></span>
          <ol class="digest__list" aria-hidden="true">
            {#each digestRows as row (row)}
              <li><span class="skel"></span></li>
            {/each}
          </ol>
        </section>
      </div>
    {/if}
  {/if}
</div>
