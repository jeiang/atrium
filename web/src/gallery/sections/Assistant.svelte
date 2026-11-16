<script lang="ts">
  import ChangeView from '../../shared/assistant/ChangeView.svelte';
  import Digest from '../../shared/assistant/Digest.svelte';
  import HoldButton from '../../shared/assistant/HoldButton.svelte';
  import ReceiptLine, { UNDO_MS } from '../../shared/assistant/ReceiptLine.svelte';
  import ScopeChip from '../../shared/assistant/ScopeChip.svelte';
  import StatusSlot from '../../shared/assistant/StatusSlot.svelte';
  import WeatherCard from '../../shared/assistant/WeatherCard.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import { digestLines, previews, weather } from '../assistant-fixtures';

  let undone = $state(0);
  let held = $state(0);
  let holdKey = $state(0);
  let confirming = $state(false);
  let staged = $state(false);
  let day = $state('today');
  const until = Date.now() + 60 * 60 * 1000;

  function onUndoReceipt(): void {
    undone += 1;
    toast('Restored the reminder');
  }

  function onUndoToast(): void {
    toast('Archived 3 messages', { action: { label: 'Undo', run: () => (undone += 1) } });
  }

  function onSent(): void {
    held += 1;
  }

  function onConfirm(): void {
    confirming = false;
    held += 1;
  }

  function onReset(): void {
    holdKey += 1;
  }
</script>

<Section id="g-receipts" data-gallery="receipts" title="Receipts, hold and status">
  <div class="stack">
    <Card>
      <ReceiptLine
        title="Reminder set for Monday 18:00"
        at="2026-11-19T09:12:00+01:00"
        undoUntil={until}
        onundo={onUndoReceipt}
      />
      <ReceiptLine title="Sent to Ilse de Wit" at="2026-11-19T09:14:00+01:00" note="It cannot be recalled" />
      <ReceiptLine title="Declined. atrium will not do this" tone="declined" at="2026-11-19T09:20:00+01:00" />
      <ReceiptLine
        title="Expired undo"
        at="2026-11-19T08:00:00+01:00"
        undoUntil={Date.now() - UNDO_MS}
        onundo={onUndoReceipt}
      />
      <p class="meta" data-testid="undo-count">Undo pressed {undone} times</p>
      <div class="cluster">
        <Button onclick={onUndoToast}>Archive with undo toast</Button>
      </div>
    </Card>
    <Card>
      <div class="cluster">
        {#key holdKey}
          <HoldButton
            label="Send the test message"
            presentation="hold"
            gated={false}
            onreveal={() => {}}
            onconfirm={() => (confirming = true)}
            onsend={onSent}
          />
        {/key}
        <Button variant="quiet" onclick={onReset}>Reset</Button>
      </div>
      <p class="meta" data-testid="hold-count">Sent {held} times</p>
    </Card>
    <Card>
      <StatusSlot text="Reading 4 messages" tone="info" action={{ label: 'Stop', run: () => toast('Stopped') }} />
      <StatusSlot text="Paused: waiting for your approval" tone="warn" />
      <div class="cluster">
        <ScopeChip
          model="Sonnet 5.5"
          effort="High"
          spent="$0.42"
          cap="$2.00"
          staged={staged ? { label: 'Opus 5', price: 'about 5x the price' } : undefined}
          onopen={() => (staged = !staged)}
        />
      </div>
    </Card>
  </div>
</Section>

<Section id="g-home" data-gallery="home" title="Digest and weather">
  <div class="grid">
    <Card><Digest lines={digestLines} title="Since this morning" /></Card>
    <Card><Digest lines={null} title="Empty digest" /></Card>
    <WeatherCard {weather} plan="Rain after 15:00. Bring the work day tools in before then.">
      {#snippet toggle()}
        <Segmented
          label="Day"
          options={[
    { value: 'today', label: 'Today' },
    { value: 'tomorrow', label: 'Tomorrow' },
  ]}
          bind:value={day}
        />
      {/snippet}
    </WeatherCard>
    <WeatherCard weather={null} />
  </div>
</Section>

<Section id="g-changes" data-gallery="changes" title="Change previews">
  <div class="grid">
    {#each previews as preview (preview.label)}
      <Card title={preview.label}>
        <ChangeView change={preview.change} plain={preview.plain} />
      </Card>
    {/each}
  </div>
</Section>

<Dialog bind:open={confirming} title="Send this now?" initialFocus="cancel">
  <p>The test message goes out at once and cannot be recalled.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="primary" onclick={onConfirm}>Send now</Button>
  {/snippet}
</Dialog>
