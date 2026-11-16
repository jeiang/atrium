<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { device } from '../shared/device.svelte';
  import { loadSession } from './session.svelte';

  interface Props {
    cached: boolean;
  }

  let { cached }: Props = $props();

  const client = useQueryClient();
  const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
    ['day', 86_400],
    ['hour', 3_600],
    ['minute', 60],
  ];

  let oldest = $state<number>();
  let now = $state(Date.now());
  let retrying = $state(false);

  $effect(() => {
    const cache = client.getQueryCache();
    const measure = (): void => {
      const stamps = cache
        .findAll({ type: 'active' })
        .map((query) => query.state.dataUpdatedAt)
        .filter((stamp) => stamp > 0);
      oldest = stamps.length > 0 ? Math.min(...stamps) : undefined;
      now = Date.now();
    };
    measure();
    const stop = cache.subscribe(measure);
    const timer = setInterval(measure, 60_000);
    return () => {
      stop();
      clearInterval(timer);
    };
  });

  const age = $derived.by(() => {
    if (oldest === undefined) return undefined;
    const seconds = Math.max(0, Math.round((now - oldest) / 1000));
    const unit = UNITS.find(([, size]) => seconds >= size);
    return unit ? relative.format(-Math.floor(seconds / unit[1]), unit[0]) : 'less than a minute ago';
  });

  const shown = $derived(age ? ` Showing data from ${age}.` : '');

  async function onRetry(): Promise<void> {
    retrying = true;
    try {
      if (cached) await loadSession(true, 'start', true);
      else await client.refetchQueries({ type: 'active' });
    } finally {
      retrying = false;
    }
  }
</script>

<div class="offline-status">
  {#if !device.online && device.offlineHint}
    <p>Offline.{shown} Nothing can be sent until you reconnect.</p>
  {:else}
    <p>Cannot reach atrium.{shown}</p>
    <button class="offline-status__retry" type="button" aria-busy={retrying} disabled={retrying} onclick={onRetry}>
      Try again
    </button>
  {/if}
</div>
