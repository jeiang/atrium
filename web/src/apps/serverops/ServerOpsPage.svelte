<script lang="ts">
  import { formatDayTime, webHref } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import { percent, severityOf, summary, upState, useServerOps } from './serverops.svelte';
  import './serverops.css';

  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'grafana'));
  const ops = useServerOps(() => app?.enabled === true);
  const data = $derived(ops.data);
  const grafana = $derived(data?.grafana_url ? webHref(data.grafana_url) : undefined);
  const connection = p('/settings/connections/:kind/:name', { params: { kind: 'service', name: 'grafana' } });
</script>

{#if appGone(apps, 'grafana')}
  <NotFound />
{:else}
  <Page title={app?.name ?? 'Server ops'} lede={data ? summary(data) : undefined}>
    {#snippet actions()}
      {#if grafana}
        <a class="btn btn--secondary" href={grafana} target="_blank" rel="noopener noreferrer">Open Grafana</a>
      {:else if data}
        <a class="btn btn--secondary" href={connection}>Grafana connection</a>
      {/if}
    {/snippet}
    <a class="link ops-back" href={p('/apps')}>Apps</a>
    {#if unreachable(ops)}
      <ViewState kind="offline" title="Cannot reach atrium">Server health loads when atrium answers again.</ViewState>
    {:else if ops.isError && !data}
      <ViewState kind="error" title="Server health did not load">
        {ops.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={() => void ops.refetch()}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !data}
      <Skeleton lines={6} label="Loading server health" />
    {:else}
      <section class="ops-section" aria-labelledby="ops-hosts">
        <h2 class="ops-section__title" id="ops-hosts">Hosts</h2>
        {#if data.hosts.length === 0}
          <ViewState kind="empty" title="No samples yet">
            The sampler records each watched host every 15 minutes.
            {#snippet action()}
              <a class="btn btn--secondary" href={connection}>Grafana connection</a>
            {/snippet}
          </ViewState>
        {:else}
          <ul class="ops-hosts" aria-label="Hosts">
            {#each data.hosts as host (host.host)}
              {const up = $derived(upState(host))}
              <li data-host={host.host}>
                <Card title={host.host} level={3}>
                  {#snippet meta()}
                    <StatusMark tone={up.tone}>{up.label}</StatusMark>
                  {/snippet}
                  <dl class="ops-facts">
                    <div>
                      <dt>CPU</dt>
                      <dd>{percent(host.cpu_pct)}</dd>
                    </div>
                    <div>
                      <dt>Memory</dt>
                      <dd>{percent(host.mem_pct)}</dd>
                    </div>
                    <div>
                      <dt>Disk</dt>
                      <dd>{percent(host.disk_root_pct)}</dd>
                    </div>
                    <div>
                      <dt>Failed units</dt>
                      <dd>
                        {host.failed_units === null || host.failed_units === undefined ? 'None' : Math.round(host.failed_units)}
                      </dd>
                    </div>
                  </dl>
                  {#if host.alerts.length > 0}
                    <ul class="ops-alerts" aria-label="Alerts firing on {host.host}">
                      {#each host.alerts as alert, index (`${alert}-${index}`)}
                        <li><Badge tone="error">{alert}</Badge></li>
                      {/each}
                    </ul>
                  {/if}
                  <p class="meta ops-when">Sampled {formatDayTime(host.sampled_at * 1000)}</p>
                </Card>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
      <section class="ops-section" aria-labelledby="ops-findings">
        <h2 class="ops-section__title" id="ops-findings">Findings</h2>
        {#if data.findings.length === 0}
          <ViewState kind="empty" title="No open findings"
            >The assistant files a finding when a host changes.</ViewState
          >
        {:else}
          <ul class="ops-findings" aria-label="Findings">
            {#each data.findings as finding (finding.id)}
              {const severity = $derived(severityOf(finding.severity))}
              <li class="ops-finding" data-finding={finding.fact}>
                <div class="ops-finding__head">
                  <Badge tone={severity.tone}>{severity.label}</Badge>
                  <a class="link ops-finding__title" href={p('/knowledge/:id', { params: { id: String(finding.id) } })}
                    >{finding.title}</a
                  >
                </div>
                <p class="ops-finding__body">{finding.body}</p>
                <p class="meta">{finding.fact} · {formatDayTime(finding.updated_at * 1000)}</p>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  </Page>
{/if}
