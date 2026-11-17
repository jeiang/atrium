<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { Token } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    active,
    handleIssueToken,
    handleRevokeToken,
    refreshTokens,
    tokenFailure,
    tokenMeta,
    tomorrow,
    useTokens,
  } from './tokens';

  let { locked, phone = false }: { locked?: string; phone?: boolean } = $props();

  const client = useQueryClient();
  const uid = $props.id();
  const tokens = useTokens();
  const offline = $derived(unreachable(tokens));
  const list = $derived(tokens.data?.data.tokens);
  const live = $derived(list?.filter((token) => active(token)) ?? []);
  const ended = $derived(list?.filter((token) => !active(token)) ?? []);

  let issuing = $state(false);
  let name = $state('');
  let expires = $state('');
  let agent = $state(false);
  let missing = $state(false);
  let issuePending = $state(false);
  let issueError = $state<string>();
  let secret = $state<string>();
  let issuedName = $state('');
  let copied = $state<string>();

  let revoking = $state<Token>();
  let revokeOpen = $state(false);
  let revokePending = $state(false);
  let revokeError = $state<string>();

  function onNew(): void {
    name = '';
    expires = '';
    agent = false;
    missing = false;
    issueError = undefined;
    secret = undefined;
    copied = undefined;
    issuing = true;
  }

  function onIssueClosed(): void {
    secret = undefined;
    copied = undefined;
    issuedName = '';
  }

  async function onIssue(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (issuePending || locked) return;
    const trimmed = name.trim();
    if (!trimmed) {
      missing = true;
      return;
    }
    missing = false;
    issuePending = true;
    issueError = undefined;
    try {
      const issued = await handleIssueToken({ name: trimmed, expires: expires || undefined, agent });
      issuedName = issued.token.name;
      secret = issued.secret;
      announce(`Made ${issued.token.name}`);
    } catch (error) {
      issueError = tokenFailure(error);
    } finally {
      issuePending = false;
      void refreshTokens(client);
    }
  }

  async function onCopy(): Promise<void> {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret);
      copied = 'Copied.';
    } catch {
      copied = 'Copying is blocked here. Select the token and copy it.';
    }
    announce(copied);
  }

  function onRevokeAsk(token: Token): void {
    revoking = token;
    revokeError = undefined;
    revokeOpen = true;
  }

  async function onRevoke(): Promise<void> {
    if (!revoking || revokePending || locked) return;
    const token = revoking;
    revokePending = true;
    revokeError = undefined;
    try {
      await handleRevokeToken(token.id);
      revokeOpen = false;
      toast(`Revoked ${token.name}.`);
    } catch (error) {
      revokeError = tokenFailure(error);
    } finally {
      revokePending = false;
      void refreshTokens(client);
    }
  }

  function onRetry(): void {
    void tokens.refetch();
  }
</script>

{#snippet row(
    token: Token,
  )}
  <ListRow title={token.name} meta={tokenMeta(token)}>
    {#snippet end()}
      {#if token.revoked_at}
        <Badge>Revoked</Badge>
      {:else if !active(token)}
        <Badge>Expired</Badge>
      {:else if !locked}
        <Button size="sm" onclick={() => onRevokeAsk(token)} label="Revoke {token.name}">Revoke</Button>
      {/if}
    {/snippet}
  </ListRow>
{/snippet}

<section class="settings-form tokens" aria-labelledby="{uid}-title">
  <div class="settings-form__head tokens__head">
    <h2 class="settings-form__title" id="{uid}-title">API tokens</h2>
    {#if !locked && !offline && list}
      <Button variant="secondary" size="sm" icon="plus" onclick={onNew}>New token</Button>
    {/if}
  </div>
  {#if locked}
    <p class="meta">{locked}</p>
  {/if}
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium" level={3}
      >The tokens load when atrium answers again.</ViewState
    >
  {:else if tokens.isError && !list}
    <ViewState kind="error" title="The tokens did not load" level={3}>
      {tokens.error?.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if !list}
    <Skeleton lines={3} label="Loading the tokens" />
  {:else if list.length === 0}
    <ViewState kind="empty" title="No API tokens" level={3}>
      Apps and scripts use a token to reach atrium without signing in.
    </ViewState>
  {:else}
    {#if live.length > 0}
      <List label="Active tokens">
        {#each live as token (token.id)}
          {@render row(token)}
        {/each}
      </List>
    {/if}
    {#if ended.length > 0}
      <h3 class="tokens__group">Revoked and expired</h3>
      <List label="Revoked and expired tokens">
        {#each ended as token (token.id)}
          {@render row(token)}
        {/each}
      </List>
    {/if}
  {/if}
</section>

<Dialog
  bind:open={issuing}
  title={secret ? `Copy the token for ${issuedName}` : 'New API token'}
  sheet={phone}
  pending={issuePending}
  onclose={onIssueClosed}
>
  {#if secret}
    <Notice tone="warn" variant="inline" title="Shown only now">
      atrium keeps only a fingerprint of it. If it is lost, revoke it and make a new one.
    </Notice>
    <div class="tokens__secret">
      <TextField label="Token" value={secret} readonly autocomplete="off" spellcheck="false" />
    </div>
    {#if copied}
      <p class="meta" role="status">{copied}</p>
    {/if}
  {:else}
    <form class="tokens__form" id="{uid}-form" novalidate onsubmit={onIssue}>
      <TextField
        label="Name"
        bind:value={name}
        maxlength={100}
        autocomplete="off"
        required
        error={missing ? 'Give the token a name.' : undefined}
      />
      <TextField
        label="Expires on"
        type="date"
        min={tomorrow()}
        bind:value={expires}
        help="Empty means it never expires."
      />
      <Checkbox label="For an agent" help="It signs in as an agent and cannot change settings." bind:checked={agent} />
      {#if issueError}
        <Notice tone="error" variant="inline">{issueError}</Notice>
      {/if}
    </form>
  {/if}
  {#snippet actions()}
    {#if secret}
      <Button onclick={onCopy}>Copy</Button>
      <Button variant="primary" onclick={() => (issuing = false)}>Done</Button>
    {:else}
      <Button variant="quiet" data-cancel disabled={issuePending} onclick={() => (issuing = false)}>Cancel</Button>
      <Button type="submit" form="{uid}-form" variant="primary" pending={issuePending}>Make the token</Button>
    {/if}
  {/snippet}
</Dialog>

<Dialog
  bind:open={revokeOpen}
  title="Revoke {revoking?.name ?? 'this token'}?"
  initialFocus="cancel"
  sheet={phone}
  pending={revokePending}
>
  <p class="tokens__text">Apps using it stop reaching atrium at once. This cannot be undone.</p>
  {#if revokeError}
    <Notice tone="error" variant="inline">{revokeError}</Notice>
  {/if}
  {#snippet actions()}
    <Button data-cancel disabled={revokePending} onclick={() => (revokeOpen = false)}>Cancel</Button>
    <Button variant="danger" pending={revokePending} onclick={onRevoke}>Revoke token</Button>
  {/snippet}
</Dialog>
