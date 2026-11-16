<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError } from '../../contract';
  import { refusedOffline, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    coordinate,
    refreshSettings,
    saveWeather,
    stale,
    useWeatherSettings,
    WEATHER_KEYS,
    when,
  } from './connections';

  type Key = keyof typeof WEATHER_KEYS;
  const order: Key[] = ['name', 'latitude', 'longitude'];
  const labels: Record<Key, string> = { name: 'Place name', latitude: 'Latitude', longitude: 'Longitude' };
  const checks: Record<Key, ((value: string) => string | undefined) | undefined> = {
    name: undefined,
    latitude: coordinate(90, 'latitude'),
    longitude: coordinate(180, 'longitude'),
  };

  const client = useQueryClient();
  const settings = useWeatherSettings(() => true);
  const offline = $derived(unreachable(settings));

  let draft = $state<Record<Key, string>>({ name: '', latitude: '', longitude: '' });
  let synced = $state<string>();
  let pending = $state(false);
  let result = $state<{ tone: 'ok' | 'warn' | 'error'; title: string; text: string }>();

  const base = $derived(
    Object.fromEntries(order.map((key) => [key, settings.data?.items[WEATHER_KEYS[key]]?.value ?? ''])) as Record<
      Key,
      string
    >,
  );
  const pinned = $derived(
    Object.fromEntries(order.map((key) => [key, settings.data?.items[WEATHER_KEYS[key]]?.pin_source])) as Record<
      Key,
      string | undefined
    >,
  );
  const dirty = $derived(order.some((key) => draft[key].trim() !== base[key]));
  const invalid = $derived(order.some((key) => checks[key]?.(draft[key]) !== undefined));
  const half = $derived((draft.latitude.trim() === '') !== (draft.longitude.trim() === ''));

  $effect(() => {
    const data = settings.data;
    if (!data || data.revision === synced) return;
    if (synced === undefined || !dirty) draft = { ...base };
    synced = data.revision;
  });

  async function onSave(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const data = settings.data;
    if (!data || !dirty || pending) return;
    if (invalid) {
      result = { tone: 'error', title: 'Check the location', text: 'Fix the marked fields, then save again.' };
      return;
    }
    if (half) {
      result = {
        tone: 'error',
        title: 'Check the location',
        text: 'Enter both the latitude and the longitude, or neither.',
      };
      return;
    }
    const set: Record<string, string> = {};
    const unset: string[] = [];
    for (const key of order) {
      const value = draft[key].trim();
      if (pinned[key] || value === base[key]) continue;
      if (value === '') unset.push(WEATHER_KEYS[key]);
      else set[WEATHER_KEYS[key]] = value;
    }
    pending = true;
    result = undefined;
    try {
      await saveWeather(data.revision, set, unset);
      draft = Object.fromEntries(order.map((key) => [key, draft[key].trim()])) as Record<Key, string>;
      result = {
        tone: 'ok',
        title: 'Location saved',
        text: `Saved ${when(new Date().toISOString())}. Home shows the forecast for it, and Test now checks it.`,
      };
      refreshSettings(client);
    } catch (error) {
      if (refusedOffline(error) && error instanceof Error) {
        result = { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
      } else if (stale(error)) {
        result = {
          tone: 'warn',
          title: 'The settings changed since they loaded',
          text: 'atrium reloaded them and kept your edits. Save again to replace the newer values.',
        };
        void settings.refetch();
      } else if (error instanceof ApiError) {
        result = { tone: 'error', title: 'atrium refused the location', text: `${error.message} Nothing was saved.` };
      } else {
        result = {
          tone: 'error',
          title: 'The outcome is unknown',
          text: 'The connection failed before atrium answered. atrium reloaded the settings and did not try again.',
        };
        void settings.refetch();
      }
    } finally {
      pending = false;
    }
  }

  function onRetry(): void {
    void settings.refetch();
  }
</script>

<Section id="weather-location" title="Weather location">
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">The location loads when atrium answers again.</ViewState>
  {:else if settings.isError && !settings.data}
    <ViewState kind="error" title="The location did not load">
      {settings.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !settings.data}
    <Skeleton lines={3} label="Loading the location" />
  {:else}
    <form class="stack conn-form" novalidate onsubmit={onSave}>
      {#each order as key (key)}
        {#if pinned[key]}
          <TextField label={labels[key]} value={draft[key]} readonly help="Set by the host" />
        {:else}
          <TextField
            label={labels[key]}
            bind:value={draft[key]}
            validate={checks[key]}
            inputmode={key === 'name' ? undefined : 'decimal'}
            autocomplete="off"
          />
        {/if}
      {/each}
      <div class="cluster">
        <Button type="submit" variant="primary" {pending} disabled={!dirty} reason="Nothing changed">
          Save location
        </Button>
      </div>
      {#if result}
        <Notice tone={result.tone} variant="inline" title={result.title}>{result.text}</Notice>
      {/if}
    </form>
  {/if}
</Section>
