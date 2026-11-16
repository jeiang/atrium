<script lang="ts">
  import { API_BASE } from '../../contract';
  import { runScripts } from '../../mocks/runs';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';

  interface Frame {
    id: string;
    event: string;
    data: string;
  }

  let script = $state(String(runScripts[0]?.run ?? ''));
  let speed = $state('10');
  let frames = $state<Frame[]>([]);
  let result = $state<string>();
  let playing = $state(false);
  let controller: AbortController | undefined;

  function parse(block: string, index: number): Frame | undefined {
    const fields = new Map<string, string>();
    for (const line of block.split('\n')) {
      const colon = line.indexOf(':');
      if (colon <= 0) continue;
      fields.set(line.slice(0, colon), line.slice(colon + 1).trimStart());
    }
    const event = fields.get('event');
    if (!event) return undefined;
    return { id: fields.get('id') ?? `x${index}`, event, data: fields.get('data') ?? '' };
  }

  async function onPlay(): Promise<void> {
    const chosen = runScripts.find((item) => String(item.run) === script);
    if (!chosen) return;
    controller?.abort();
    controller = new AbortController();
    frames = [];
    result = undefined;
    playing = true;
    try {
      const response = await fetch(`${API_BASE}/threads/${chosen.thread}/runs/${chosen.run}/events?speed=${speed}`, {
        headers: { Accept: 'text/event-stream' },
        signal: controller.signal,
      });
      if (!response.ok || !response.body) {
        result = `HTTP ${response.status}`;
        return;
      }
      const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
      let buffer = '';
      let count = 0;
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += value;
        let end = buffer.indexOf('\n\n');
        while (end >= 0) {
          const frame = parse(buffer.slice(0, end), count++);
          buffer = buffer.slice(end + 2);
          if (frame) {
            frames.push(frame);
            if (frame.event === 'done') result = (JSON.parse(frame.data) as { state: string }).state;
          }
          end = buffer.indexOf('\n\n');
        }
      }
    } catch (error) {
      if (!(error instanceof DOMException && error.name === 'AbortError')) result = 'Connection lost';
    } finally {
      playing = false;
    }
  }
</script>

<Section id="g-simulator" data-gallery="simulator" title="Run stream simulator">
  <div class="cluster">
    <Segmented
      label="Script"
      options={runScripts.map((item) => ({ value: String(item.run), label: item.label }))}
      bind:value={script}
    />
    <Segmented
      label="Speed"
      options={[
    { value: '0', label: 'Instant' },
    { value: '1', label: '1x' },
    { value: '10', label: '10x' },
  ]}
      bind:value={speed}
    />
    <Button variant="primary" pending={playing} onclick={onPlay}>{playing ? 'Playing' : 'Play'}</Button>
  </div>
  {#if result}
    <p data-testid="run-result">
      <Badge tone={result === 'done' ? 'ok' : result === 'parked' ? 'warn' : 'error'}>{result}</Badge>
    </p>
  {/if}
  {#if frames.length > 0}
    <List label="Run events" class="sim-events">
      {#each frames as frame (frame.id)}
        <ListRow title={frame.event} meta={frame.data} />
      {/each}
    </List>
  {/if}
</Section>
