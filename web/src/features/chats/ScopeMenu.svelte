<script lang="ts">
  import type { ModelConfiguration } from '../../contract';
  import Menu, { type MenuItem } from '../../shared/ui/Menu.svelte';

  interface Props {
    options: ModelConfiguration | undefined;
    model: string;
    effort: string;
    staged: boolean;
    onchange: (model: string, effort: string) => void;
  }

  let { options, model, effort, staged, onchange }: Props = $props();

  let phone = $state(false);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    const update = (event: MediaQueryListEvent): void => {
      phone = event.matches;
    };
    query.addEventListener('change', update);
    return () => query.removeEventListener('change', update);
  });

  const current = $derived(options?.models.find((entry) => entry.id === model));
  const price = $derived(current ? `$${current.rates.output} per million output tokens` : '');
  const label = $derived(`${current?.name ?? model} · ${effort}${staged ? `, from the next message, ${price}` : ''}`);
  const items = $derived<MenuItem[]>([
    ...(options?.models ?? []).map(
      (entry): MenuItem => ({
        value: `model:${entry.id}`,
        label: `${entry.name}, $${entry.rates.output} per million out`,
        icon: entry.id === model ? 'check' : undefined,
      }),
    ),
    ...(current?.efforts ?? []).map(
      (value): MenuItem => ({
        value: `effort:${value}`,
        label: `Effort ${value}`,
        icon: value === effort ? 'check' : undefined,
      }),
    ),
  ]);

  function onSelect(value: string): void {
    const [kind, choice = ''] = value.split(':');
    if (kind === 'model') {
      const next = options?.models.find((entry) => entry.id === choice);
      const nextEffort = next && !next.efforts.includes(effort) ? (next.efforts.at(-1) ?? effort) : effort;
      onchange(choice, nextEffort);
    } else {
      onchange(model, choice);
    }
  }
</script>

<div class="scope" class:scope--staged={staged}>
  <Menu
    label={phone ? `Model: ${label}` : label}
    {items}
    icon="assistant"
    iconOnly={phone}
    variant="quiet"
    size="sm"
    onselect={onSelect}
  />
</div>

<style>
  .scope {
    display: flex;
    min-width: 0;
  }

  .scope--staged :global(.btn) {
    box-shadow: inset 0 0 0 1px var(--honey-line);
  }
</style>
