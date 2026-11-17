<script lang="ts">
  import type { KnowledgeSubjectRow } from '../../contract';
  import { formatDay, plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { isActive, p } from '../../shell/router';
  import FactDialog from './FactDialog.svelte';
  import { KINDS, type SubjectFilter, useInstructions, useSubjects } from './knowledge';

  interface Props {
    selected?: number;
    standing?: boolean;
    level?: 2 | 3;
    phone?: boolean;
  }

  let { selected, standing = false, level = 2, phone = false }: Props = $props();
  let creating = $state(false);

  const kindOptions = [
    { value: '', label: 'All kinds' },
    { value: 'wiki', label: 'Pages' },
    { value: 'fact', label: 'Facts' },
    { value: 'lesson', label: 'Lessons' },
  ];
  const scopeOptions = [
    { value: '', label: 'Work and personal' },
    { value: 'work', label: 'Work' },
    { value: 'personal', label: 'Personal' },
  ];

  let text = $state('');
  let kind = $state('');
  let scope = $state('');
  let q = $state('');

  $effect(() => {
    const next = text.trim();
    const timer = setTimeout(() => (q = next), 250);
    return () => clearTimeout(timer);
  });

  const subjects = useSubjects(() => ({ q, kind, scope }) as SubjectFilter);
  const instructions = useInstructions();
  const standingId = $derived(instructions.data?.record.id);
  const filtered = $derived(q !== '' || kind !== '' || scope !== '');
  const rows = $derived(
    (subjects.data?.pages.flatMap((page) => page.items) ?? []).filter((row) => row.id !== standingId),
  );
  const offline = $derived(unreachable(subjects));

  function meta(row: KnowledgeSubjectRow): string {
    const parts = [KINDS[row.kind] ?? row.kind];
    if (row.facts > 0) parts.push(plural(row.facts, 'fact'));
    parts.push(`edited ${formatDay(row.updated_at)}`);
    return parts.join(' · ');
  }

  function onMore(): void {
    void subjects.fetchNextPage();
  }

  function onRetry(): void {
    void subjects.refetch();
  }
</script>

<div class="subjects">
  <div class="subjects__head">
    <svelte:element this={`h${level}`} class="subjects__title">Subjects</svelte:element>
    <Button size="sm" icon="plus" onclick={() => (creating = true)}>New fact</Button>
  </div>
  <div class="subjects__filters">
    <TextField label="Find a subject" type="search" bind:value={text} autocomplete="off" />
    <div class="subjects__selects">
      <SelectField label="Kind" options={kindOptions} bind:value={kind} />
      <SelectField label="Scope" options={scopeOptions} bind:value={scope} />
    </div>
  </div>
  {#if instructions.data && !filtered}
    <List label="Pinned" class="subjects__pinned">
      <ListRow
        title={instructions.data.record.title || 'Standing instructions'}
        meta="Read on every run"
        href={p('/knowledge/instructions')}
        selected={standing}
      >
        {#snippet end()}
          <Badge tone="accent">Pinned</Badge>
        {/snippet}
      </ListRow>
      <ListRow
        title="Review"
        meta="Writes waiting for you to confirm"
        href={p('/knowledge/review')}
        selected={isActive('/knowledge/review')}
      />
      <ListRow
        title="About me"
        meta="Pinned facts read on every run"
        href={p('/knowledge/me')}
        selected={isActive('/knowledge/me')}
      />
      <ListRow title="Spaces" meta="Subjects, chats and files by project" href={p('/knowledge/spaces')} />
      <ListRow title="Files" meta="Every file in chats and spaces" href={p('/knowledge/files')} />
    </List>
  {/if}
  <div class="subjects__region" aria-busy={subjects.isFetching || undefined}>
    {#if offline}
      <ViewState kind="offline" title="Cannot reach atrium" level={3}>
        Subjects load again when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if subjects.isPending}
      <ViewState kind="loading" title="Loading subjects" />
    {:else if subjects.isError && rows.length === 0}
      <ViewState kind="error" title="Subjects could not be loaded" level={3}>
        {subjects.error.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if rows.length === 0}
      <ViewState kind="empty" title={filtered ? 'No subject matches' : 'Nothing saved yet'} level={3}>
        {filtered
    ? 'Try other words, or show every kind and scope.'
    : 'Pages, facts and lessons appear here when you or the assistant save them.'}
      </ViewState>
    {:else}
      <List label="Subjects">
        {#each rows as row (row.id)}
          <ListRow
            title={row.title}
            meta={meta(row)}
            href={p('/knowledge/:id', { params: { id: String(row.id) } })}
            selected={row.id === selected && !standing}
          >
            {#snippet end()}
              {#if !row.confirmed}
                <Badge tone="warn">Unconfirmed</Badge>
              {/if}
              {#if row.private}
                <Badge>Private</Badge>
              {/if}
            {/snippet}
          </ListRow>
        {/each}
      </List>
      {#if subjects.hasNextPage}
        <Button class="subjects__more" pending={subjects.isFetchingNextPage} onclick={onMore}>Show more</Button>
      {/if}
      {#if subjects.isError}
        <p class="subjects__note" role="status">More subjects could not be loaded. {subjects.error.message}</p>
      {/if}
    {/if}
  </div>
  <FactDialog bind:open={creating} {phone} />
</div>
