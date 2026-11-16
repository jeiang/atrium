<script lang="ts">
  import './assistant.css';

  interface Props {
    model: string;
    effort: string;
    spent?: string;
    cap?: string;
    staged?: { label: string; price: string };
    expanded?: boolean;
    onopen: () => void;
  }

  let { model, effort, spent, cap, staged, expanded, onopen }: Props = $props();

  const cost = $derived(spent && cap ? `${spent} of ${cap} cap` : spent ? `${spent} so far` : cap ? `${cap} cap` : '');
</script>

<button type="button" class="scope" aria-expanded={expanded} onclick={onopen}>
  <span class="scope__line">{model} · {effort}</span>
  {#if cost}
    <span>{cost}</span>
  {/if}
  {#if staged}
    <span class="scope__staged">{staged.label} from the next message · {staged.price}</span>
  {/if}
</button>
