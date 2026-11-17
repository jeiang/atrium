<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import {
    ApiError,
    type ConnectionView,
    connectionsDisconnect,
    connectionsSaveFields,
    connectionsTest,
    connectionsTestSave,
    ok,
  } from '../../contract';
  import { refusedOffline, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import DataTable, { type Column } from '../../shared/ui/DataTable.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate, p, route } from '../../shell/router';
  import ConnectionState from './ConnectionState.svelte';
  import {
    configEntries,
    type Draft,
    fieldsBody,
    fieldsFor,
    gesture,
    graceText,
    hostSlot,
    normalized,
    ownerText,
    reasonText,
    refreshConnections,
    remember,
    repairHint,
    slotLabel,
    splitId,
    stale,
    useConnections,
    useSchema,
    WEATHER_ID,
    when,
    words,
  } from './connections';
  import SecretField from './SecretField.svelte';
  import WeatherLocation from './WeatherLocation.svelte';
  import './connections.css';

  type Outcome = { tone: 'ok' | 'warn' | 'error'; title: string; text: string };
  type Busy = 'test' | 'test-save' | 'save' | 'disconnect';

  const client = useQueryClient();
  const list = useConnections();
  const schema = useSchema();
  const kind = $derived(route.params.kind ?? '');
  const name = $derived(route.params.name ?? '');
  const id = $derived(`${kind}/${name}`);
  const view = $derived(list.data?.find((row) => row.id === id));
  const fields = $derived(fieldsFor(schema.data, id));
  const offline = $derived(unreachable(list));
  const textFields = $derived(fields.filter((field) => !field.secret));
  const secretFields = $derived(
    fields
      .filter((field) => field.secret)
      .map((field) => ({ field, stored: view?.credentials.find((item) => item.slot === field.name) })),
  );
  const editable = $derived(fields.some((field) => !field.host_owned && !field.owner));
  const config = $derived(configEntries(view?.config).filter(([key]) => key !== 'url'));
  const currentUrl = $derived(configEntries(view?.config).find(([key]) => key === 'url')?.[1] ?? '');
  const hostDeclared = $derived(
    view?.credentials.some((credential) => !credential.owner && (credential.pinned || hostSlot(credential.source))) ??
      false,
  );
  const ownCredentials = $derived(view?.credentials.filter((credential) => !credential.owner) ?? []);
  const removable = $derived(
    view !== undefined &&
      view.config_source === 'operator' &&
      !hostDeclared &&
      (ownCredentials.some((credential) => credential.present) || configEntries(view.config).length > 0),
  );
  const storedSlots = $derived(ownCredentials.filter((credential) => credential.present).map((c) => slotLabel(c.slot)));
  const hint = $derived(repairHint(view));

  let label = $state('');
  let url = $state('');
  let secrets = $state<Record<string, string>>({});
  let loadedFor = $state<string>();
  let busy = $state<Busy>();
  let saved = $state<Outcome>();
  let tested = $state<Outcome>();
  let confirming = $state(false);
  let removeFailed = $state<string>();

  const dirty = $derived(
    view !== undefined &&
      ((textFields.some((field) => field.name === 'label' && !field.host_owned) && label.trim() !== view.label) ||
        (textFields.some((field) => field.name === 'url' && !field.host_owned) && url.trim() !== currentUrl) ||
        Object.values(secrets).some((value) => value !== '')),
  );

  $effect(() => {
    if (!view || loadedFor === id) return;
    label = view.label;
    url = currentUrl;
    secrets = {};
    saved = undefined;
    tested = undefined;
    loadedFor = id;
  });

  function draft(): Draft {
    const out: Draft = { credentials: {} };
    for (const field of fields) {
      if (field.host_owned || field.owner) continue;
      if (field.name === 'label' && label.trim() !== view?.label) out.label = label.trim();
      else if (field.name === 'url' && url.trim() !== currentUrl) out.url = url.trim();
      else if (field.secret && secrets[field.name]) out.credentials[field.name] = secrets[field.name] as string;
    }
    return out;
  }

  function failure(error: unknown, action: string): Outcome {
    if (refusedOffline(error) && error instanceof Error) {
      return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
    }
    if (
      error instanceof ApiError &&
      error.code === 'refused' &&
      error.message.includes('pinned by host configuration')
    ) {
      refreshConnections(client);
      return {
        tone: 'error',
        title: 'This credential is set in host configuration',
        text: 'atrium saved nothing. Change the credential in host configuration, then Test now.',
      };
    }
    if (stale(error)) {
      refreshConnections(client);
      return {
        tone: 'warn',
        title: 'This connection changed since it loaded',
        text: 'atrium reloaded it and kept your edits. Check it, then try again.',
      };
    }
    if (error instanceof ApiError) {
      return { tone: 'error', title: `atrium refused the ${action}`, text: `${error.message}` };
    }
    refreshConnections(client);
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered. atrium reloaded the connection and did not try again.',
    };
  }

  function settle(next: ConnectionView, title: string): void {
    remember(client, next);
    label = next.label;
    url = configEntries(next.config).find(([key]) => key === 'url')?.[1] ?? '';
    secrets = {};
    saved = {
      tone: 'ok',
      title,
      text: `${words(next.state, next.grace_until).label} at ${when(next.checked_at ?? new Date().toISOString())}.`,
    };
    refreshConnections(client);
  }

  async function onTestAndSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!view || !dirty || busy) return;
    busy = 'test-save';
    saved = undefined;
    try {
      const stored = ok(await connectionsTestSave(kind, name, fieldsBody(draft()), gesture())).data;
      settle(normalized(stored), 'Tested and saved');
    } catch (error) {
      saved =
        error instanceof ApiError && error.status === 422
          ? { tone: 'error', title: 'Not saved: the test failed', text: `${error.message} Your edits are still here.` }
          : failure(error, 'save');
    } finally {
      busy = undefined;
    }
  }

  async function onSaveOnly(): Promise<void> {
    if (!view || !dirty || busy) return;
    busy = 'save';
    saved = undefined;
    try {
      const next = normalized(ok(await connectionsSaveFields(kind, name, fieldsBody(draft()), gesture())).data);
      settle(next, 'Saved without testing');
    } catch (error) {
      saved = failure(error, 'save');
    } finally {
      busy = undefined;
    }
  }

  async function onTest(): Promise<void> {
    if (!view || busy) return;
    busy = 'test';
    tested = undefined;
    try {
      const views = ok(await connectionsTest(kind, name, gesture())).data.connections;
      const found = views.find((row) => row.id === id);
      const next = found ? normalized(found) : undefined;
      if (!next) {
        refreshConnections(client);
        tested = {
          tone: 'warn',
          title: 'Tested',
          text: 'atrium did not return this connection. It reloaded the list.',
        };
        return;
      }
      remember(client, next);
      const shown = words(next.state, next.grace_until);
      const at = when(next.checked_at ?? new Date().toISOString());
      tested =
        next.state === 'ok'
          ? { tone: 'ok', title: 'Test passed', text: `Every capability answered at ${at}.` }
          : {
              tone: next.state === 'failing' && !next.grace_until ? 'error' : 'warn',
              title: `Test result: ${shown.label}`,
              text: `${reasonText(next) || 'No reason was given.'} Checked ${at}.`,
            };
    } catch (error) {
      tested = failure(error, 'test');
    } finally {
      busy = undefined;
    }
  }

  function onAskDisconnect(): void {
    removeFailed = undefined;
    confirming = true;
  }

  function onCloseDialog(): void {
    if (busy !== 'disconnect') confirming = false;
  }

  async function onDisconnect(): Promise<void> {
    if (!view || busy) return;
    busy = 'disconnect';
    removeFailed = undefined;
    const gone = view.label;
    try {
      ok(await connectionsDisconnect(kind, name, gesture()));
      confirming = false;
      refreshConnections(client);
      toast(`Disconnected ${gone}. Its stored credentials are deleted.`);
      void navigate('/settings/connections');
    } catch (error) {
      const outcome = failure(error, 'disconnect');
      removeFailed = `${outcome.title}. ${outcome.text}`;
    } finally {
      busy = undefined;
    }
  }

  function onRetry(): void {
    void list.refetch();
    void schema.refetch();
  }

  const resultColumns: readonly Column[] = [
    { key: 'capability', label: 'Capability' },
    { key: 'state', label: 'Result' },
    { key: 'reason', label: 'Error or detail' },
    { key: 'used', label: 'In use' },
  ];

  interface ResultRow {
    capability: string;
    state: string;
    reason: string;
    grace?: string;
    expires?: string;
    used: string;
  }

  const results = $derived.by((): ResultRow[] => {
    if (!view) return [];
    const names = [...new Set([...view.capabilities.map((c) => c.name), ...view.results.map((r) => r.capability)])];
    return names.map((name) => {
      const result = view.results.find((item) => item.capability === name);
      const capability = view.capabilities.find((item) => item.name === name);
      return {
        capability: name,
        state: result?.state ?? 'untested',
        reason: result?.reason ?? (result ? '' : 'Not tested yet.'),
        grace: result?.grace_until,
        expires: result?.expires_at,
        used: capability ? (capability.enabled ? 'Yes' : 'No') : 'Not declared',
      };
    });
  });
</script>

{#snippet resultCell(
    row: ResultRow,
    column: Column,
  )}
  {#if column.key === 'capability'}
    <span class="conn-name">{row.capability}</span>
  {:else if column.key === 'state'}
    <ConnectionState state={row.state} graceUntil={row.grace} />
  {:else if column.key === 'reason'}
    <span class="meta conn-reason">
      {row.reason}
      {#if row.expires}
        Expires {when(row.expires)}.
      {/if}
    </span>
  {:else}
    <span class="meta"><span class="conn-prefix">In use </span>{row.used}</span>
  {/if}
{/snippet}

<Page title={view?.label ?? 'Connection'} eyebrow="Connections">
  <a class="link conn-back" href={p('/settings/connections')}>All connections</a>
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">
      This connection loads when atrium answers again. Nothing was changed.
    </ViewState>
  {:else if list.isError && !list.data}
    <ViewState kind="error" title="The connection did not load">
      {list.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !list.data}
    <Skeleton lines={6} label="Loading the connection" />
  {:else if !view}
    <ViewState kind="empty" title="No such connection">
      atrium has no connection named {id}.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/settings/connections')}>All connections</a>
      {/snippet}
    </ViewState>
  {:else}
    <Section id="connection-status" title="Status">
      <dl class="facts">
        <div>
          <dt>State</dt>
          <dd class="conn-status">
            <ConnectionState state={view.state} graceUntil={view.grace_until} />
            {#if reasonText(view)}
              <span class="conn-reason">{reasonText(view)}</span>
            {/if}
          </dd>
        </div>
        {#if view.grace_until}
          <div>
            <dt>Grace</dt>
            <dd>{graceText(view.grace_until)}</dd>
          </div>
        {/if}
        <div>
          <dt>Enabled</dt>
          <dd>
            {#if view.enabled}
              Yes
            {:else}
              No. atrium does not use or test it until the host file or its app turns it back on.
            {/if}
          </dd>
        </div>
        <div>
          <dt>Kind</dt>
          <dd>{view.kind}</dd>
        </div>
        <div>
          <dt>Owner</dt>
          <dd>{ownerText(view)}{view.config_source === 'host' ? ', set by the host file' : ''}</dd>
        </div>
        <div>
          <dt>Last checked</dt>
          <dd>{when(view.checked_at)}</dd>
        </div>
        <div>
          <dt>Last OK</dt>
          <dd>{when(view.last_ok_at)}</dd>
        </div>
        {#if view.expires_at}
          <div>
            <dt>Expires</dt>
            <dd>{when(view.expires_at)}</dd>
          </div>
        {/if}
        {#if view.reauth_at}
          <div>
            <dt>Sign in again by</dt>
            <dd>{when(view.reauth_at)}</dd>
          </div>
        {/if}
      </dl>
      {#if view.state === 'needs_reauth'}
        <Notice tone="warn" variant="inline" title="To repair">
          {#if hint?.owner}
            This connection uses the credential of {hint.owner}.
            {hint.pinned
              ? 'That credential is changed in host configuration; change it there, then Test now.'
              : 'Change it on that connection, then Test now.'}
          {:else if hint?.pinned}
            The credential is changed in host configuration. Change it there, then Test now.
          {:else}
            Sign in again in the current interface, then come back and Test now.
          {/if}
          {#snippet action()}
            {#if hint?.owner}
              <a
                class="btn btn--secondary"
                href={p('/settings/connections/:kind/:name', { params: splitId(hint.owner) })}
                >Open {hint.owner}</a
              >
            {:else if !hint?.pinned}
              <a class="btn btn--secondary" href="/settings/connections">Open Connections in the current interface</a>
            {/if}
          {/snippet}
        </Notice>
      {:else if view.state === 'unconfigured' && view.enabled}
        <Notice tone="warn" variant="inline" title="To repair">
          {editable
    ? 'Fill in what is missing below, then choose Test and save.'
    : 'The host file declares this connection. Add what is missing there, then Test now.'}
        </Notice>
      {:else if view.state === 'failing' || view.state === 'expiring'}
        <Notice tone="warn" variant="inline" title="To repair">
          {hostDeclared || view.config_source === 'host'
    ? 'Check the service and the host file values, then Test now.'
    : 'Check the service and the fields below, then Test now or Test and save new values.'}
        </Notice>
      {/if}
      <div class="cluster">
        <Button
          variant={view.state === 'ok' ? 'secondary' : 'primary'}
          pending={busy === 'test'}
          disabled={!view.enabled || (busy !== undefined && busy !== 'test')}
          reason={view.enabled ? undefined : 'The connection is off'}
          onclick={onTest}
        >
          Test now
        </Button>
      </div>
      {#if busy === 'test'}
        <p class="meta" role="status">Testing {view.label}</p>
      {/if}
      {#if tested}
        <Notice tone={tested.tone} variant="inline" title={tested.title}>{tested.text}</Notice>
      {/if}
    </Section>

    <Section id="connection-results" title="Test results" meta="Last checked {when(view.checked_at)}">
      <DataTable
        caption="Test results"
        columns={resultColumns}
        rows={results}
        key={(row) => row.capability}
        cell={resultCell}
        empty="This connection has no capabilities."
      />
    </Section>

    <Section id="connection-fields" title="Fields">
      {#if !schema.data && schema.isError}
        <ViewState kind="error" title="The fields did not load">
          {schema.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={onRetry}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !schema.data}
        <Skeleton lines={3} label="Loading the fields" />
      {:else}
        <form class="stack conn-form" novalidate onsubmit={onTestAndSave}>
          {#each textFields as field (field.name)}
            {#if field.name === 'label'}
              {#if field.host_owned}
                <TextField label="Name" value={view.label} readonly help="Set by the host" />
              {:else}
                <TextField label="Name" bind:value={label} autocomplete="off" />
              {/if}
            {:else if field.name === 'url'}
              {#if field.host_owned}
                <TextField label="Address" type="url" value={currentUrl} readonly help="Set by the host" />
              {:else}
                <TextField label="Address" type="url" bind:value={url} autocomplete="off" />
              {/if}
            {/if}
          {/each}
          {#each secretFields as { field, stored } (field.name)}
            {#if field.owner}
              <dl class="facts facts--single">
                <div>
                  <dt>{slotLabel(field.name)}</dt>
                  <dd class="stack">
                    <span
                      >Uses the credential of {field.owner}.
                      {field.pinned
                        ? 'It is set in host configuration.'
                        : 'Change it on that connection.'}</span
                    >
                    <a
                      class="btn btn--secondary"
                      href={p('/settings/connections/:kind/:name', { params: splitId(field.owner) })}
                      >Open {field.owner}</a
                    >
                  </dd>
                </div>
              </dl>
            {:else if field.host_owned}
              <dl class="facts facts--single">
                <div>
                  <dt>{slotLabel(field.name)}</dt>
                  <dd>
                    Provided by the host{stored?.file_ref ? ` from ${stored.file_ref}` : ''}.
                    {stored?.present ? '' : 'The host file declares it, but no value is there.'}
                    {stored?.expires_at ? `Expires ${when(stored.expires_at)}.` : ''}
                  </dd>
                </div>
              </dl>
            {:else}
              <SecretField
                label={slotLabel(field.name)}
                bind:value={() => secrets[field.name] ?? '', (value) => (secrets = { ...secrets, [field.name]: value })}
                help={stored?.present
    ? `Stored ${when(stored.set_at)}${stored.set_by ? ` by ${stored.set_by}` : ''}${stored.expires_at ? `, expires ${when(stored.expires_at)}` : ''}. A new value replaces it.`
    : 'Not set.'}
              />
            {/if}
          {/each}
          {#if config.length > 0}
            <dl class="facts">
              {#each config as [key, value] (key)}
                <div>
                  <dt>{slotLabel(key)}</dt>
                  <dd>
                    <span class="conn-value">{value}</span>
                    <span class="meta"
                      >{view.config_source === 'host' ? 'Set by the host' : 'Set when it was connected'}</span
                    >
                  </dd>
                </div>
              {/each}
            </dl>
          {/if}
          {#if editable}
            <div class="cluster">
              <Button
                type="submit"
                variant="primary"
                pending={busy === 'test-save'}
                disabled={!dirty || (busy !== undefined && busy !== 'test-save')}
                reason={dirty ? undefined : 'Nothing changed'}
              >
                Test and save
              </Button>
              <Button
                pending={busy === 'save'}
                disabled={!dirty || (busy !== undefined && busy !== 'save')}
                onclick={onSaveOnly}
              >
                Save without testing
              </Button>
            </div>
            {#if saved}
              <Notice tone={saved.tone} variant="inline" title={saved.title}>{saved.text}</Notice>
            {/if}
          {/if}
        </form>
      {/if}
    </Section>

    {#if view.id === WEATHER_ID}
      <WeatherLocation />
    {/if}

    {#if removable}
      <Section id="connection-disconnect" title="Disconnect">
        <div class="cluster">
          <Button variant="danger" disabled={busy !== undefined} onclick={onAskDisconnect}
            >Disconnect {view.label}</Button
          >
        </div>
      </Section>
      <Dialog
        bind:open={confirming}
        title="Disconnect {view.label}?"
        pending={busy === 'disconnect'}
        initialFocus="cancel"
      >
        <p>
          This deletes {storedSlots.length > 0 ? `the stored ${storedSlots.join(', ')}` : 'its stored credentials'},
          clears its configuration and test results, and turns it off. It cannot be undone.
        </p>
        {#if removeFailed}
          <Notice tone="error" variant="inline">{removeFailed}</Notice>
        {/if}
        {#snippet actions()}
          <Button data-cancel disabled={busy === 'disconnect'} onclick={onCloseDialog}>Cancel</Button>
          <Button variant="danger" pending={busy === 'disconnect'} onclick={onDisconnect}>Disconnect</Button>
        {/snippet}
      </Dialog>
    {/if}
  {/if}
</Page>
