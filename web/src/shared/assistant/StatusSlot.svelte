<script lang="ts">
  import type { Tone } from '../ui/Badge.svelte';
  import Button from '../ui/Button.svelte';
  import StatusMark from '../ui/StatusMark.svelte';
  import './assistant.css';

  interface Props {
    text?: string;
    tone?: Tone;
    action?: { label: string; run: () => void };
  }

  let { text, tone = 'neutral', action }: Props = $props();
</script>

<div class="status-slot" role="status">
  {#if text}
    <StatusMark {tone}>{text}</StatusMark>
    {#if action}
      <Button variant="quiet" size="sm" onclick={action.run}>{action.label}</Button>
    {/if}
  {/if}
</div>
