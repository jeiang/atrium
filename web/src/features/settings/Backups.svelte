<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError, type BackupEntry, maintenanceBackup, maintenanceVerifyArchive, ok } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { formatClock, formatDayTime, plural } from '../../shared/assistant/format';
  import { refusedOffline, unreachable } from '../../shared/device.svelte';
  import { refreshSettings, useBackups } from '../../shared/settings/settings';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  let { locked }: { locked?: string } = $props();

  type Result = { tone: 'ok' | 'warn' | 'error'; title: string; text: string };

  const client = useQueryClient();
  const backups = useBackups();
  const offline = $derived(unreachable(backups));
  const data = $derived(backups.data?.data);
  const ready = $derived(data?.location.state === 'ready');

  let creating = $state(false);
  let verifying = $state<string>();
  let result = $state<Result>();
  let checks = $state<Record<string, Result>>({});

  const problems: Record<string, string> = {
    not_archive: 'Not an atrium archive',
    old_format: 'Made by an older atrium; this version cannot read it',
    newer_format: 'Made by a newer atrium; this version cannot read it',
  };

  function size(bytes: number): string {
    if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`;
    if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(1)} MB`;
    return `${Math.max(1, Math.round(bytes / 1e3))} kB`;
  }

  function meta(entry: BackupEntry): string {
    const made = formatDayTime(entry.header?.created_at ?? entry.modified_at);
    if (entry.problem) return `${made}, ${size(entry.size_bytes)}. ${problems[entry.problem] ?? entry.problem}`;
    const header = entry.header;
    const parts = [made, size(entry.size_bytes)];
    if (header) {
      parts.push(plural(header.files, 'file'));
      parts.push(header.encryption.encrypted ? 'encrypted' : 'not encrypted');
    }
    return parts.join(', ');
  }

  function failure(error: unknown, what: string): Result {
    if (refusedOffline(error) && error instanceof Error) {
      return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
    }
    if (error instanceof ApiError) return { tone: 'error', title: `${what} failed`, text: error.message };
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered. atrium reloaded the list and did not try again.',
    };
  }

  async function onCreate(): Promise<void> {
    if (creating || locked) return;
    creating = true;
    result = undefined;
    try {
      const made = ok(await maintenanceBackup({ headers: { 'Idempotency-Key': crypto.randomUUID() } })).data;
      result = {
        tone: 'ok',
        title: `Backed up at ${formatClock(new Date())}`,
        text: `${made.name}, ${plural(made.files, 'file')}, ${made.encryption.encrypted ? 'encrypted' : 'not encrypted'}.`,
      };
      announce(result.title);
    } catch (error) {
      result = failure(error, 'The backup');
    } finally {
      creating = false;
      refreshSettings(client);
    }
  }

  async function onVerify(entry: BackupEntry): Promise<void> {
    if (verifying || locked) return;
    verifying = entry.name;
    try {
      const checked = ok(
        await maintenanceVerifyArchive(
          { name: entry.name },
          { headers: { 'Idempotency-Key': crypto.randomUUID() } },
        ),
      ).data;
      checks[entry.name] = {
        tone: 'ok',
        title: `Verified at ${formatClock(new Date())}`,
        text: `It opens and restores ${plural(checked.files, 'file')} at schema ${checked.to_schema}.`,
      };
      announce(`${entry.name} verified`);
    } catch (error) {
      checks[entry.name] = failure(error, 'The check');
      announce(`${entry.name} did not verify`);
    } finally {
      verifying = undefined;
    }
  }

  function onRetry(): void {
    void backups.refetch();
  }
</script>

{#if offline}
  <ViewState kind="offline" title="Cannot reach atrium">The backups load when atrium answers again.</ViewState>
{:else if backups.isError && !data}
  <ViewState kind="error" title="The backups did not load">
    {backups.error.message}
    {#snippet action()}
      <Button variant="secondary" onclick={onRetry}>Retry</Button>
    {/snippet}
  </ViewState>
{:else if !data}
  <Skeleton lines={3} label="Loading the backups" />
{:else}
  {#if !ready}
    <ViewState kind="blocked" title={data.location.state === 'missing' ? 'The backup folder is missing' : 'Backups are not set up'}>
      Set by the host: {data.location.nix_option} ({data.location.host_key}).
    </ViewState>
  {:else}
    <div class="cluster">
      <Button variant="primary" pending={creating} disabled={locked !== undefined} reason={locked} onclick={onCreate}>
        Back up now
      </Button>
      <span class="meta">Folder set by the host: {data.location.nix_option}</span>
    </div>
  {/if}
  {#if result}
    <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
  {/if}
  {#if data.items.length === 0}
    {#if ready}
      <ViewState kind="empty" title="No backups yet">Back up now makes the first archive.</ViewState>
    {/if}
  {:else}
    <List label="Backups" class="backups">
      {#each data.items as entry (entry.name)}
        <ListRow title={entry.name} meta={meta(entry)} pending={verifying === entry.name}>
          {#snippet end()}
            {#if checks[entry.name]}
              <Badge tone={checks[entry.name]?.tone === 'ok' ? 'ok' : 'error'}>{checks[entry.name]?.title}</Badge>
            {:else if entry.problem}
              <Badge tone="warn">Unreadable</Badge>
            {/if}
            {#if !entry.problem}
              <Button
                variant="secondary"
                size="sm"
                pending={verifying === entry.name}
                disabled={locked !== undefined || (verifying !== undefined && verifying !== entry.name)}
                reason={locked}
                onclick={() => onVerify(entry)}
              >
                Verify<span class="visually-hidden"> {entry.name}</span>
              </Button>
            {/if}
          {/snippet}
        </ListRow>
      {/each}
    </List>
    {#each data.items as entry (entry.name)}
      {#if checks[entry.name] && checks[entry.name]?.tone !== 'ok'}
        <Notice tone={checks[entry.name]?.tone} variant="inline" title={`${entry.name}: ${checks[entry.name]?.title}`}>
          {checks[entry.name]?.text}
        </Notice>
      {/if}
    {/each}
  {/if}
{/if}
