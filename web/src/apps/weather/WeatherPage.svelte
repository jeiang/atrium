<script lang="ts">
  import { ageText, failureText } from '../../features/home/weather';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import { dayText, degrees, HOURS_SHOWN, hourText, useWeather, windText } from './weather.svelte';
  import './weather.css';

  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'weather'));
  const forecast = useWeather(() => app?.enabled === true);
  const data = $derived(forecast.data);
  const place = p('/settings/connections/:kind/:name', { params: { kind: 'service', name: 'weather' } });
  const staleMinutes = $derived(
    data?.state === 'stale' && data.fetched_at
      ? Math.max(0, Math.floor((Date.now() / 1000 - data.fetched_at) / 60))
      : undefined,
  );
</script>

{#if appGone(apps, 'weather')}
  <NotFound />
{:else}
  <Page title={app?.name ?? 'Weather'} lede={data?.location ?? undefined}>
    {#snippet actions()}
      <a class="btn btn--secondary" href={place}>Change location</a>
    {/snippet}
    <a class="link wx-back" href={p('/apps')}>Apps</a>
    {#if unreachable(forecast)}
      <ViewState kind="offline" title="Cannot reach atrium">The forecast loads when atrium answers again.</ViewState>
    {:else if forecast.isError && !data}
      <ViewState kind="error" title="The forecast did not load">
        {forecast.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={() => void forecast.refetch()}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !data || data.state === 'loading'}
      <Skeleton lines={6} label="Loading the forecast" />
    {:else if data.state === 'off'}
      <ViewState kind="empty" title="The forecast is off">
        Turn on the forecast in the weather connection.
        {#snippet action()}
          <a class="btn btn--secondary" href={place}>Weather connection</a>
        {/snippet}
      </ViewState>
    {:else if data.state === 'unconfigured'}
      <ViewState kind="empty" title="No location set">
        Set a place to see its forecast.
        {#snippet action()}
          <a class="btn btn--secondary" href={place}>Set a location</a>
        {/snippet}
      </ViewState>
    {:else if data.state === 'failed' || !data.current}
      <ViewState kind="error" title="The forecast did not load">
        {failureText(data.failure ?? undefined)}
        {#snippet action()}
          <Button variant="secondary" onclick={() => void forecast.refetch()}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else}
      {#if staleMinutes !== undefined}
        <Notice tone="warn" variant="inline" title="Not updated recently">
          Last updated {ageText(staleMinutes)}.
        </Notice>
      {/if}
      <div class="wx-grid">
        <Card title="Now" level={2}>
          <p class="wx-now">
            <span class="wx-now__temp">{degrees(data.current.temperature_c)}</span>
            <span>{data.current.condition}</span>
            <span class="meta">Feels like {degrees(data.current.apparent_c)}</span>
          </p>
          <dl class="wx-facts">
            {#if data.days[0]}
              <div>
                <dt>High</dt>
                <dd>{degrees(data.days[0].high_c)}</dd>
              </div>
              <div>
                <dt>Low</dt>
                <dd>{degrees(data.days[0].low_c)}</dd>
              </div>
            {/if}
            <div>
              <dt>Humidity</dt>
              <dd>{Math.round(data.current.humidity)}%</dd>
            </div>
            <div>
              <dt>Wind</dt>
              <dd>{windText(data.current.wind_kmh, data.current.wind_direction)}</dd>
            </div>
            {#if data.days[0]?.sunrise && data.days[0]?.sunset}
              <div>
                <dt>Sunrise</dt>
                <dd>{hourText(data.days[0].sunrise)}</dd>
              </div>
              <div>
                <dt>Sunset</dt>
                <dd>{hourText(data.days[0].sunset)}</dd>
              </div>
            {/if}
          </dl>
        </Card>
        {#if data.hours.length > 0}
          <Card title="Next hours" level={2}>
            <ol class="wx-hours" aria-label="Next hours">
              {#each data.hours.slice(0, HOURS_SHOWN) as hour (hour.time)}
                <li class="wx-hour">
                  <span class="wx-hour__time">{hourText(hour.time)}</span>
                  <span class="wx-hour__temp">{degrees(hour.temperature_c)}</span>
                  <span class="visually-hidden">{hour.condition},</span>
                  <span class="wx-hour__rain"
                    >{Math.round(hour.precipitation_probability)}%<span class="visually-hidden">
                      chance of rain</span
                    ></span
                  >
                </li>
              {/each}
            </ol>
          </Card>
        {/if}
      </div>
      {#if data.days.length > 0}
        <Card title="Next {data.days.length} days" level={2}>
          <ul class="wx-days" aria-label="Days">
            {#each data.days as day, index (day.date)}
              <li class="wx-day">
                <span class="wx-day__name">{dayText(day.date, index)}</span>
                <span class="wx-day__condition">{day.condition}</span>
                <span class="wx-day__rain"
                  >{Math.round(day.precipitation_probability)}%<span class="visually-hidden">
                    chance of rain</span
                  ></span
                >
                <span class="wx-day__range">{degrees(day.low_c)} / {degrees(day.high_c)}</span>
              </li>
            {/each}
          </ul>
        </Card>
      {/if}
    {/if}
  </Page>
{/if}
