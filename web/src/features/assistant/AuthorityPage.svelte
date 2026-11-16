<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { automationPause, automationResume, ok, type PermissionRule, reviewPermissionsUpdate } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { inboxPath } from '../../shared/assistant/inboxId';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useShell } from '../../shell/frame';
  import { p } from '../../shell/router';
  import AssistantNav from './AssistantNav.svelte';
  import {
    dateTime,
    failure,
    fieldErrors,
    gesture,
    type Outcome,
    refresh,
    usePermissionRequests,
    usePermissions,
  } from './assistant';
  import './assistant.css';

  const EDITABLE = ['people', 'internet'] as const;
  const channelWords: Record<string, { title: string; lede: string }> = {
    people: {
      title: 'Other people',
      lede: 'Messages, replies and invitations that reach someone other than you.',
    },
    internet: {
      title: 'Internet',
      lede: 'Fetching pages and calling outside services.',
    },
  };
  const situationWords: Record<string, string> = {
    clean: 'When nothing in the thread is marked',
    kinds: 'When the thread holds messages or personal notes',
    pii: 'When the thread holds contact details',
    untrusted_knowledge: 'When the thread holds outside text',
    untrusted_kinds: 'When the thread holds outside text and private data',
    untrusted_isolated: 'In an isolated helper',
  };
  const decisionWords: Record<string, string> = { allow: 'Allow', ask: 'Ask me first', deny: 'Refuse' };
  const statusWords: Record<string, { label: string; tone: 'ok' | 'warn' | 'error' | 'neutral' | 'info' }> = {
    pending: { label: 'Waiting for you', tone: 'warn' },
    approved: { label: 'Allowed', tone: 'ok' },
    applied: { label: 'Allowed', tone: 'ok' },
    dismissed: { label: 'Refused', tone: 'neutral' },
    failed: { label: 'Failed', tone: 'error' },
    suppressed: { label: 'Expired', tone: 'neutral' },
  };

  const client = useQueryClient();
  const permissions = usePermissions();
  const requests = usePermissionRequests();
  const offline = $derived(unreachable(permissions));
  const requestsOffline = $derived(unreachable(requests));
  const shell = useShell();
  const pause = $derived(shell.data?.pause);
  let pausing = $state(false);
  let pauseResult = $state<Outcome>();

  async function onPauseToggle(): Promise<void> {
    if (pausing || !pause) return;
    const resume = pause.paused;
    pausing = true;
    pauseResult = undefined;
    try {
      if (resume) ok(await automationResume(gesture()));
      else ok(await automationPause({}, gesture()));
      announce(resume ? 'Automation resumed' : 'Automation paused');
    } catch (error) {
      pauseResult = failure(error, resume ? 'the resume' : 'the pause', () => void shell.refetch());
    } finally {
      pausing = false;
      void client.invalidateQueries({ queryKey: ['shell'] });
    }
  }

  let draft = $state<Record<string, string>>({});
  let synced = $state<string>();
  let pending = $state(false);
  let errors = $state<Record<string, string>>({});
  let result = $state<Outcome>();

  const rules = $derived(permissions.data?.data.rules ?? []);
  const editable = $derived(rules.filter((rule) => (EDITABLE as readonly string[]).includes(rule.channel)));
  const fixed = $derived(rules.filter((rule) => !(EDITABLE as readonly string[]).includes(rule.channel)));
  const base = $derived(Object.fromEntries(editable.map((rule) => [rule.key, rule.value])));
  const dirty = $derived(editable.some((rule) => (draft[rule.key] ?? '') !== base[rule.key]));
  const ownAtOnce = $derived(fixed.length > 0 && fixed.every((rule) => rule.effective === 'allow'));
  const ownExceptions = $derived(fixed.filter((rule) => rule.effective !== 'allow'));

  $effect(() => {
    const data = permissions.data;
    if (!data || data.revision === synced) return;
    if (synced === undefined || !dirty) draft = { ...base };
    synced = data.revision;
  });

  function options(rule: PermissionRule) {
    return rule.choices.map((choice) => ({ value: choice, label: decisionWords[choice] ?? choice }));
  }

  function help(rule: PermissionRule): string {
    const parts: string[] = [];
    if (rule.floor !== 'allow')
      parts.push(`Never looser than ${(decisionWords[rule.floor] ?? rule.floor).toLowerCase()} here.`);
    if (rule.source === 'pin') parts.push('Set by the host.');
    else if (rule.source === 'default') parts.push('atrium default.');
    return parts.join(' ');
  }

  function rulesFor(channel: string): PermissionRule[] {
    return editable.filter((rule) => rule.channel === channel);
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!permissions.data || !dirty || pending) return;
    const changes = editable
      .filter((rule) => (draft[rule.key] ?? '') !== base[rule.key])
      .map((rule) => ({ key: rule.key, value: draft[rule.key] ?? '' }));
    pending = true;
    result = undefined;
    errors = {};
    try {
      ok(await reviewPermissionsUpdate({ changes }, gesture()));
      result = {
        tone: 'ok',
        title: 'Authority saved',
        text: 'The next tool call checks the new table. Runs waiting for you are checked again when you decide.',
      };
      refresh(client, 'settings');
    } catch (error) {
      errors = fieldErrors(error);
      result = failure(error, 'the change', () => void permissions.refetch());
    } finally {
      pending = false;
    }
  }
</script>

<Page
  title="Authority"
  eyebrow="Assistant"
  lede="What the assistant may do on its own, what it asks you about, and what it never does."
>
  <AssistantNav current="authority" />
  <div class="asst">
    <Section id="always" title="Always true">
      <ul class="asst-facts" aria-label="Fixed rules">
        <li>Only you send email. The assistant can write drafts; sending one needs you.</li>
        <li>
          Outside a chat you started, "Ask me first" refuses the action, unless the action already waits for your
          approval in the Inbox.
        </li>
        <li>Once outside text is in a thread, actions toward other people or the internet can only ask or refuse.</li>
      </ul>
    </Section>

    <Section id="automation" title="Automation">
      {#if pause}
        <div class="cluster asst-pause">
          <StatusMark tone={pause.paused ? 'warn' : 'ok'}>{pause.paused ? 'Paused' : 'Running'}</StatusMark>
          {#if pause.paused && pause.since}
            <span class="meta">Since {dateTime(pause.since)}{pause.by ? ` by ${pause.by}` : ''}</span>
          {/if}
          <Button variant={pause.paused ? 'primary' : 'secondary'} pending={pausing} onclick={onPauseToggle}
            >{pause.paused ? 'Resume automation' : 'Pause automation'}</Button
          >
        </div>
        {#if pause.paused && pause.reason}
          <p class="asst-text">{pause.reason}</p>
        {/if}
        {#if pauseResult}
          <Notice tone={pauseResult.tone} variant="inline" title={pauseResult.title}>{pauseResult.text}</Notice>
        {/if}
      {:else}
        <Skeleton lines={1} label="Loading the pause state" />
      {/if}
    </Section>

    {#if offline}
      <Section id="table" title="Permissions">
        <ViewState kind="offline" title="Cannot reach atrium" level={3}
          >The permissions load when atrium answers again.</ViewState
        >
      </Section>
    {:else if permissions.isError && !permissions.data}
      <Section id="table" title="Permissions">
        <ViewState kind="error" title="The permissions did not load" level={3}>
          {permissions.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void permissions.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      </Section>
    {:else if !permissions.data}
      <Section id="table" title="Permissions">
        <Skeleton lines={6} label="Loading the permissions" />
      </Section>
    {:else if rules.length === 0}
      <Section id="table" title="Permissions">
        <ViewState kind="empty" title="No permissions" level={3}>atrium reports no permission table.</ViewState>
      </Section>
    {:else}
      <Section id="own" title="Your accounts and notifications">
        <p class="asst-text" data-own={ownAtOnce ? 'at-once' : 'mixed'}>
          {#if ownAtOnce}
            Changes to your own accounts and notifications to you happen at once. They appear on Done, and you can undo
            them there.
          {:else}
            Changes to your own accounts and notifications to you happen at once and can be undone on Done, except:
            {ownExceptions.map((rule) => situationWords[rule.situation]?.toLowerCase() ?? rule.situation).join('; ')}.
          {/if}
        </p>
      </Section>
      <form class="asst-authority" novalidate onsubmit={onSave} data-settings="authority">
        {#each EDITABLE as channel (channel)}
          <Section id="channel-{channel}" title={channelWords[channel]?.title ?? channel}>
            <p class="asst-text">{channelWords[channel]?.lede}</p>
            <div class="stack asst-form">
              {#each rulesFor(channel) as rule (rule.key)}
                <div class="asst-form__field" data-key={rule.key}>
                  {#if rule.source === 'pin'}
                    <SelectField
                      label={situationWords[rule.situation] ?? rule.situation}
                      value={draft[rule.key] ?? ''}
                      options={options(rule)}
                      disabled
                      help={help(rule)}
                    />
                  {:else}
                    <SelectField
                      label={situationWords[rule.situation] ?? rule.situation}
                      bind:value={() => draft[rule.key] ?? '', (next) => (draft[rule.key] = next)}
                      options={options(rule)}
                      help={help(rule)}
                      error={errors[rule.key] ?? errors.changes}
                    />
                  {/if}
                </div>
              {/each}
            </div>
          </Section>
        {/each}
        <div class="cluster asst-authority__save">
          <Button type="submit" variant="primary" {pending} disabled={!dirty} reason="Nothing changed"
            >Save authority</Button
          >
        </div>
        {#if result}
          <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
        {/if}
      </form>
    {/if}

    <Section
      id="requests"
      title="Permission requests"
      meta={permissions.data ? `${permissions.data.data.pending_requests} waiting` : undefined}
    >
      {#if requestsOffline}
        <ViewState kind="offline" title="Cannot reach atrium" level={3}
          >The requests load when atrium answers again.</ViewState
        >
      {:else if requests.isError && !requests.data}
        <ViewState kind="error" title="The requests did not load" level={3}>
          {requests.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void requests.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !requests.data}
        <Skeleton lines={3} label="Loading the requests" />
      {:else if requests.data.length === 0}
        <ViewState kind="empty" title="No requests yet" level={3}>
          When the assistant asks before acting, the request shows here and in the Inbox.
        </ViewState>
      {:else}
        <List label="Recent permission requests">
          {#each requests.data as request (request.id)}
            <ListRow title={request.title} href={inboxPath('proposal', request.id)}>
              {#snippet end()}
                <Badge tone={statusWords[request.status]?.tone ?? 'neutral'}>
                  {statusWords[request.status]?.label ?? request.status}
                </Badge>
              {/snippet}
            </ListRow>
          {/each}
        </List>
        <div class="cluster">
          <a class="asst-link" href={p('/inbox')}>Open the Inbox</a>
        </div>
      {/if}
    </Section>
  </div>
</Page>
