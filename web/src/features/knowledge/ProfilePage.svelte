<script lang="ts">
  import type { KnowledgeProfileEntry } from '../../contract';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import FactDialog from './FactDialog.svelte';
  import { useProfile } from './knowledge';

  let { phone }: { phone: boolean } = $props();

  const profile = useProfile();
  const data = $derived(profile.data);
  const offline = $derived(unreachable(profile));
  let adding = $state(false);

  const count = new Intl.NumberFormat('en-US');
  const REASONS: Record<string, string> = {
    unconfirmed: 'Left out: unconfirmed',
    marked: 'Left out: marked sensitive',
    private: 'Left out: private',
    'over budget': 'Left out: over budget',
  };

  function status(entry: KnowledgeProfileEntry): { label: string; tone: 'ok' | 'warn' | 'neutral' } {
    if (entry.in_prompt) return { label: 'In every run', tone: 'ok' };
    return {
      label: REASONS[entry.reason] ?? `Left out: ${entry.reason}`,
      tone: entry.reason === 'private' ? 'neutral' : 'warn',
    };
  }

  function meta(entry: KnowledgeProfileEntry): string {
    return entry.tokens > 0 ? `${entry.body} (${count.format(entry.tokens)} tokens)` : entry.body;
  }

  function onRetry(): void {
    void profile.refetch();
  }
</script>

<section class="kreview" aria-labelledby="kprofile-title">
  <header class="kreview__head">
    <h1 class="page-title" id="kprofile-title">About me</h1>
    <p class="kreview__lede">Pinned facts about you that the assistant reads at the start of every run.</p>
    <div class="kreview__bar">
      <Button variant="primary" icon="plus" onclick={() => (adding = true)}>Add a fact about me</Button>
    </div>
  </header>
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium" level={2}>
      Your profile loads when the connection returns.
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if profile.isPending}
    <div class="kpage-links-loading"><ViewState kind="loading" title="Loading your profile" /></div>
  {:else if profile.isError || !data}
    <ViewState kind="error" title="Your profile could not be loaded" level={2}>
      {profile.error?.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if data.entries.length === 0}
    <ViewState kind="empty" title="Nothing about you yet" level={2}>
      Add facts about yourself and the assistant reads them on every run.
    </ViewState>
  {:else}
    <p class="kprofile__budget">
      Uses {count.format(data.tokens)} of {count.format(data.budget)} tokens ({data.percent}%).
    </p>
    <Section
      title="Facts about you"
      meta="{data.entries.filter((entry) => entry.in_prompt).length} of {data.entries.length} in every run"
    >
      <List label="Facts about you">
        {#each data.entries as entry (entry.id)}
          <ListRow
            title={entry.title || 'Untitled'}
            meta={meta(entry)}
            href={p('/knowledge/:id', { params: { id: String(entry.id) } })}
          >
            {#snippet end()}
              <Badge tone={status(entry).tone}>{status(entry).label}</Badge>
            {/snippet}
          </ListRow>
        {/each}
      </List>
    </Section>
    {#if data.block}
      <details class="kprofile__block">
        <summary>What the assistant reads</summary>
        <pre>{data.block}</pre>
      </details>
    {/if}
  {/if}
</section>
<FactDialog bind:open={adding} {phone} pinned />
