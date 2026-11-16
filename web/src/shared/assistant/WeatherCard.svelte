<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HomeWeather } from './types';
  import './assistant.css';

  interface Props {
    weather: HomeWeather | null;
    plan?: string;
    toggle?: Snippet;
    note?: Snippet;
    level?: 2 | 3;
  }

  let { weather, plan, toggle, note, level = 3 }: Props = $props();

  const uid = $props.id();

  function degrees(value: number): string {
    return `${Math.round(value)}°`;
  }
</script>

<section class="card weather" aria-labelledby="{uid}-title">
  <div class="card__head">
    <svelte:element this={`h${level}`} class="card__title" id="{uid}-title"
      >Weather{weather?.location ? ` in ${weather.location}` : ''}</svelte:element
    >
    {@render toggle?.()}
  </div>
  {#if weather?.current}
    <div class="weather__now">
      <span class="weather__temp">{degrees(weather.current.temperature_c)}</span>
      <span>{weather.current.condition}</span>
      <span class="meta">Feels like {degrees(weather.current.apparent_c)}</span>
    </div>
    {#if weather.today}
      <dl class="weather__facts">
        <div>
          <dt>High</dt>
          <dd>{degrees(weather.today.high_c)}</dd>
        </div>
        <div>
          <dt>Low</dt>
          <dd>{degrees(weather.today.low_c)}</dd>
        </div>
        <div>
          <dt>Rain today</dt>
          <dd>{weather.today.precipitation_probability}%</dd>
        </div>
      </dl>
    {/if}
    {#if plan}
      <p class="weather__plan">{plan}</p>
    {/if}
    {@render note?.()}
  {:else if note}
    {@render note()}
  {:else}
    <p class="weather__plan">No weather yet. Set a location in Settings to see it here.</p>
  {/if}
</section>
