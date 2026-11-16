<script lang="ts">
  import { API_BASE, type RunStreamEvent } from '../../contract';
  import { runScripts } from '../../mocks/runs';
  import ApprovalCard from '../../shared/assistant/ApprovalCard.svelte';
  import ToolCard from '../../shared/assistant/ToolCard.svelte';
  import { applyRunEvent } from '../../shared/assistant/toolcalls';
  import type { Approval, ToolCall } from '../../shared/assistant/types';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import { draftApproval } from '../assistant-fixtures';
  import { readStream } from '../stream';

  const parked: Approval = {
    ...draftApproval,
    id: 'a-parked',
    title: 'Send the book club reminder to 6 people',
    from: 'Send the book club reminder',
    change: {
      kind: 'mail_draft',
      account: 'sam@example.test',
      to: 'book-club@example.test',
      cc: '',
      subject: 'Book club on Thursday',
      body: 'Hi all,\n\nA reminder that we meet on Thursday at 19:00 in the library reading room.\n\nSam',
      unexpected_recipients: [],
    },
    consequence: { ...draftApproval.consequence, audience: '6 people on the book club list' },
    primary: 'Send email to 6 people',
    done: 'Sent to 6 people',
    sending: 'Sending to 6 people',
  };

  const samples: ToolCall[] = [
    {
      id: 's1',
      name: 'search_web',
      args: '{"query":"seed swap Hillfield"}',
      state: 'running',
      progress: 'Reading 2 pages',
    },
    {
      id: 's2',
      name: 'search_web',
      args: '{"query":"winter tyre quotes"}',
      state: 'done',
      preview: 'Found 2 quotes',
      results: [
        { title: 'Tyre Hall: winter sets', url: 'https://tyre-hall.example/winter' },
        { title: 'https://riverside-garage.example/quote', url: 'https://riverside-garage.example/quote' },
      ],
    },
    {
      id: 's3',
      name: 'calendar.read',
      args: '{"days":7}',
      state: 'failed',
      preview: 'The calendar server refused the login',
    },
  ];

  let script = $state(String(runScripts[0]?.run ?? ''));
  let calls = $state<ToolCall[]>([]);
  let result = $state<string>();
  let playing = $state(false);
  let controller: AbortController | undefined;

  async function onPlay(): Promise<void> {
    const chosen = runScripts.find((item) => String(item.run) === script);
    if (!chosen) return;
    controller?.abort();
    controller = new AbortController();
    calls = [];
    result = undefined;
    playing = true;
    try {
      const url = `${API_BASE}/threads/${chosen.thread}/runs/${chosen.run}/events?speed=2`;
      await readStream(url, controller.signal, (frame) => {
        const event = { event: frame.event, id: frame.id, data: JSON.parse(frame.data) } as RunStreamEvent;
        calls = applyRunEvent(calls, event, chosen.run);
        if (frame.event === 'done') result = (event.data as { state: string }).state;
      });
    } catch (error) {
      if (!(error instanceof DOMException && error.name === 'AbortError')) result = 'Connection lost';
    } finally {
      playing = false;
    }
  }

  function settle(): Promise<void> {
    return new Promise((resolve) => window.setTimeout(resolve, 300));
  }
</script>

<Section id="g-tools" data-gallery="tools" title="Tool cards">
  <div class="stack">
    {#each samples as call (call.id)}
      <ToolCard {call} />
    {/each}
    <div class="cluster">
      <Segmented
        label="Run"
        options={runScripts.map((item) => ({ value: String(item.run), label: item.label }))}
        bind:value={script}
      />
      <Button variant="primary" pending={playing} onclick={onPlay}>{playing ? 'Running' : 'Run script'}</Button>
      {#if result}
        <span data-testid="tool-run-result"
          ><Badge tone={result === 'done' ? 'ok' : result === 'parked' ? 'warn' : 'error'}>{result}</Badge></span
        >
      {/if}
    </div>
    <div class="stack" data-testid="tool-live">
      {#each calls as call (call.id)}
        <ToolCard {call}>
          {#snippet approval()}
            <ApprovalCard approval={parked} level={4} onapprove={settle} ondecline={settle} onundo={settle} />
          {/snippet}
        </ToolCard>
      {/each}
    </div>
  </div>
</Section>
