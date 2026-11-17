<script lang="ts">
  import { createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    ApiError,
    notificationsChannelContent,
    notificationsConfiguration,
    notificationsSubscribe,
    notificationsTest,
    notificationsUnsubscribe,
    OFFLINE_MESSAGE,
    ok,
    type PushDevice,
  } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { formatClock, formatDay, formatDayTime, plural } from '../../shared/assistant/format';
  import { device, refusedOffline, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import Switch from '../../shared/ui/Switch.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { type DeviceState, readDevice, type Subscribed, subscribe, unsubscribe } from './push';

  let { locked, phone = false }: { locked?: string; phone?: boolean } = $props();

  type Result = { tone: 'ok' | 'warn' | 'error'; title: string; text?: string };

  const TOPICS = ['notify', 'settings', 'connections'] as const;
  const FAILING = 3;
  const CONTENT = [
    { value: 'auto', label: 'Full text (default)' },
    { value: 'full', label: 'Full text' },
    { value: 'title', label: 'Only “atrium”, no text' },
  ];

  const client = useQueryClient();
  const configuration = createQuery(() => ({
    queryKey: ['notifications', 'configuration'],
    queryFn: async ({ signal }) => ok(await notificationsConfiguration({ signal })),
    meta: { topics: TOPICS },
  }));
  const offline = $derived(unreachable(configuration));
  const data = $derived(configuration.data);
  const devices = $derived(data?.push.devices ?? []);

  let here = $state<DeviceState>();
  let busy = $state<'switch' | 'test' | 'remove' | 'channels'>();
  let attempt = $state(0);
  let pushResult = $state<Result>();
  let testResult = $state<Result>();
  let channelResult = $state<Result>();
  let removing = $state<PushDevice>();
  let confirming = $state(false);
  let removeFailed = $state<Result>();
  let choices = $state<Record<string, string>>({});

  const mine = $derived(
    here && devices.find((item) => item.endpoint === here?.endpoint || item.endpoint === here?.classic),
  );
  const classic = $derived(mine !== undefined && here?.classic !== undefined && mine.endpoint === here.classic);
  const switchReason = $derived.by(() => {
    if (locked) return locked;
    if (here?.support === 'unsupported') return 'This browser cannot receive push.';
    if (here?.support === 'install') {
      return 'On iPhone and iPad, push works only in atrium opened from the Home Screen. Add it there with Share, then Add to Home Screen.';
    }
    if (data && !data.push.key) return `Push is not set up on the server: ${data.push.problem || 'no key'}.`;
    if (here?.permission === 'denied' && !mine) {
      return 'Notifications are blocked for this site. Allow them in the browser’s site settings.';
    }
    return undefined;
  });
  const changed = $derived(
    (data?.channels ?? []).filter((channel) => (choices[channel.name] ?? channel.content) !== channel.content),
  );

  $effect(() => {
    void readDevice().then((state) => (here = state));
  });

  function refresh(): void {
    void client.invalidateQueries({ queryKey: ['notifications'] });
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

  function unreachableNow(): Result | undefined {
    if (device.online) return undefined;
    return { tone: 'warn', title: 'Cannot reach atrium', text: OFFLINE_MESSAGE };
  }

  function meta(item: PushDevice): string {
    const parts = [`Added ${formatDay(item.created_ts)}`];
    if (item.last_error) parts.push(`last attempt failed: ${item.last_error}`);
    else if (item.last_ok_ts) parts.push(`last delivered ${formatDayTime(item.last_ok_ts)}`);
    return parts.join(', ');
  }

  async function browserSubscribe(key: string): Promise<Subscribed | undefined> {
    try {
      return await subscribe(key);
    } catch (error) {
      here = await readDevice();
      pushResult =
        here.permission === 'denied'
          ? { tone: 'warn', title: 'Notifications are blocked for this site' }
          : {
              tone: 'error',
              title: 'The browser did not turn push on',
              text: error instanceof Error ? error.message : String(error),
            };
      return undefined;
    }
  }

  async function onSwitch(on: boolean): Promise<void> {
    if (busy || switchReason || !data) return;
    pushResult = unreachableNow();
    if (pushResult) {
      attempt++;
      return;
    }
    busy = 'switch';
    const registered = mine;
    try {
      if (on) {
        const made = await browserSubscribe(data.push.key);
        if (made) {
          ok(await notificationsSubscribe(made, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
          pushResult = { tone: 'ok', title: 'Push is on for this device' };
        }
      } else {
        if (registered) {
          ok(await notificationsUnsubscribe(registered.id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
        }
        const endpoint = registered?.endpoint ?? here?.endpoint;
        if (endpoint) await unsubscribe(endpoint);
        pushResult = { tone: 'ok', title: 'Push is off for this device' };
      }
      if (pushResult?.tone === 'ok') announce(pushResult.title);
    } catch (error) {
      pushResult = failure(error, on ? 'Turning push on' : 'Turning push off');
    } finally {
      here = await readDevice();
      busy = undefined;
      attempt++;
      refresh();
    }
  }

  async function onTest(): Promise<void> {
    if (busy || locked) return;
    testResult = unreachableNow();
    if (testResult) return;
    busy = 'test';
    try {
      const sent = ok(await notificationsTest({ headers: { 'Idempotency-Key': crypto.randomUUID() } }));
      testResult = { tone: 'ok', title: `Test sent to ${plural(sent.sent, 'device')} at ${formatClock(new Date())}` };
      announce(testResult.title);
    } catch (error) {
      testResult = failure(error, 'The test');
    } finally {
      busy = undefined;
      refresh();
    }
  }

  function onAskRemove(item: PushDevice): void {
    removeFailed = undefined;
    removing = item;
    confirming = true;
  }

  async function onRemove(): Promise<void> {
    const item = removing;
    if (!item || busy || locked) return;
    removeFailed = unreachableNow();
    if (removeFailed) return;
    busy = 'remove';
    try {
      ok(await notificationsUnsubscribe(item.id, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
      if (item.endpoint === here?.endpoint || item.endpoint === here?.classic) await unsubscribe(item.endpoint);
      confirming = false;
      announce(`${item.label || 'The device'} removed`);
    } catch (error) {
      removeFailed = failure(error, 'Removing the device');
    } finally {
      busy = undefined;
      here = await readDevice();
      refresh();
    }
  }

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (busy || locked || changed.length === 0) return;
    channelResult = unreachableNow();
    if (channelResult) return;
    busy = 'channels';
    let saved = 0;
    try {
      for (const channel of changed) {
        const content = choices[channel.name] ?? channel.content;
        ok(
          await notificationsChannelContent(
            channel.name,
            { content },
            { headers: { 'Idempotency-Key': crypto.randomUUID() } },
          ),
        );
        saved++;
      }
      channelResult = { tone: 'ok', title: `Saved at ${formatClock(new Date())}` };
      announce(channelResult.title);
      choices = {};
    } catch (error) {
      const result = failure(error, 'Saving');
      channelResult =
        saved > 0
          ? { ...result, text: `${result.text ?? ''} ${plural(saved, 'channel')} saved before it.`.trim() }
          : result;
    } finally {
      busy = undefined;
      refresh();
    }
  }

  function onRetry(): void {
    void configuration.refetch();
  }
</script>

{#if offline}
  <ViewState kind="offline" title="Cannot reach atrium"
    >The notification settings load when atrium answers again.</ViewState
  >
{:else if configuration.isError && !data}
  <ViewState kind="error" title="The notification settings did not load">
    {configuration.error.message}
    {#snippet action()}
      <Button variant="secondary" onclick={onRetry}>Retry</Button>
    {/snippet}
  </ViewState>
{:else if !data || !here}
  <Skeleton lines={4} label="Loading the notification settings" />
{:else}
  {#if data.error}
    <Notice tone="error" variant="inline">{data.error}</Notice>
  {/if}
  <section class="notify__group" aria-labelledby="notify-device">
    <h3 class="notify__title" id="notify-device">This device</h3>
    {#key attempt}
      <Switch
        label="Push on this device"
        checked={mine !== undefined}
        pending={busy === 'switch'}
        locked={switchReason}
        onchange={onSwitch}
      />
    {/key}
    {#if classic}
      <p class="meta">Turned on from the classic pages. Turning it off here stops push on this device.</p>
    {/if}
    {#if pushResult}
      <Notice tone={pushResult.tone} variant="inline" title={pushResult.title}>{pushResult.text ?? ''}</Notice>
    {/if}
    <div class="cluster">
      <Button
        variant="secondary"
        pending={busy === 'test'}
        disabled={locked !== undefined || devices.length === 0 || (busy !== undefined && busy !== 'test')}
        reason={locked ?? (devices.length === 0 ? 'No device has push on yet.' : undefined)}
        onclick={onTest}
      >
        Send a test push
      </Button>
    </div>
    {#if testResult}
      <Notice tone={testResult.tone} variant="inline" title={testResult.title}>{testResult.text ?? ''}</Notice>
    {/if}
  </section>

  <section class="notify__group" aria-labelledby="notify-devices">
    <h3 class="notify__title" id="notify-devices">Devices with push</h3>
    {#if devices.length === 0}
      <ViewState kind="empty" title="No device gets push">Turn push on above to add this one.</ViewState>
    {:else}
      <List label="Devices with push">
        {#each devices as item (item.id)}
          <ListRow title={item.label || 'Unnamed device'} meta={meta(item)}>
            {#snippet end()}
              {#if item === mine}
                <Badge tone="accent">This device</Badge>
              {/if}
              {#if item.failure_count >= FAILING}
                <Badge tone="warn">Failing</Badge>
              {/if}
              <Button
                variant="secondary"
                size="sm"
                disabled={locked !== undefined || busy !== undefined}
                reason={locked}
                onclick={() => onAskRemove(item)}
              >
                Remove<span class="visually-hidden"> {item.label || 'Unnamed device'}</span>
              </Button>
            {/snippet}
          </ListRow>
        {/each}
      </List>
    {/if}
  </section>

  <form class="notify__group" aria-labelledby="notify-channels" novalidate onsubmit={onSave}>
    <h3 class="notify__title" id="notify-channels">What each channel shows</h3>
    {#each data.channels as channel (channel.name)}
      <SelectField
        label={channel.title}
        options={CONTENT}
        bind:value={() => choices[channel.name] ?? channel.content, (next) => (choices[channel.name] = next)}
        disabled={locked !== undefined || busy === 'channels'}
      />
    {/each}
    {#if channelResult}
      <Notice tone={channelResult.tone} variant="inline" title={channelResult.title}>{channelResult.text ?? ''}</Notice>
    {/if}
    <div class="cluster">
      <Button
        type="submit"
        variant="primary"
        pending={busy === 'channels'}
        disabled={locked !== undefined || changed.length === 0}
        reason={locked}
      >
        Save
      </Button>
    </div>
  </form>
{/if}

<Dialog
  bind:open={confirming}
  title="Remove {removing?.label || 'this device'}?"
  pending={busy === 'remove'}
  initialFocus="cancel"
  sheet={phone}
>
  <p>It stops getting push. Turning push on from that device adds it again.</p>
  {#if removeFailed}
    <Notice tone={removeFailed.tone} variant="inline" title={removeFailed.title}>{removeFailed.text ?? ''}</Notice>
  {/if}
  {#snippet actions()}
    <Button data-cancel disabled={busy === 'remove'} onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="danger" pending={busy === 'remove'} onclick={onRemove}>Remove device</Button>
  {/snippet}
</Dialog>
