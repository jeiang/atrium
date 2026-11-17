<script lang="ts">
  import { formatDayTime } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import { useCandidates } from './calendar.svelte';

  const candidates = useCandidates();
  const offline = $derived(unreachable(candidates));
</script>

<section class="cal-candidates" aria-labelledby="cal-candidates-head">
  <h2 class="cal-head" id="cal-candidates-head">Proposed events</h2>
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium" level={3}>
      Proposed events load when atrium answers again.
    </ViewState>
  {:else if candidates.isError && !candidates.data}
    <ViewState kind="error" title="The proposals did not load" level={3}>
      {candidates.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void candidates.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !candidates.data}
    <Skeleton lines={3} label="Loading the proposals" />
  {:else if candidates.data.length === 0}
    <ViewState kind="empty" title="No proposed events" level={3}>
      Events atrium finds in mail wait here for your decision.
    </ViewState>
  {:else}
    <List label="Proposed events">
      {#each candidates.data as item (item.id)}
        <ListRow
          title={item.title || '(no title)'}
          meta="{item.summary} · {formatDayTime(item.arrived_at * 1000)}"
          href={p('/inbox/:item', { params: { item: item.id } })}
        />
      {/each}
    </List>
  {/if}
</section>
