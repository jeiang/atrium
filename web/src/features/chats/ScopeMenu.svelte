<script lang="ts">
  import type { ModelConfiguration } from '../../contract';
  import Menu, { type MenuItem } from '../../shared/ui/Menu.svelte';
  import { contextChoices, contextLabel, type Scope, snapContext } from './scope';

  interface Props {
    options: ModelConfiguration | undefined;
    value: Scope;
    fixed?: boolean;
    staged?: boolean;
    onchange: (next: Scope) => void;
  }

  let { options, value: scope, fixed = false, staged = false, onchange }: Props = $props();

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

  const current = $derived(options?.models.find((entry) => entry.id === scope.model));
  const contexts = $derived(contextChoices(options, current));
  const price = $derived(current ? `$${current.rates.output} per million output tokens` : '');
  const label = $derived(
    `${current?.name ?? scope.model} · ${scope.effort} · ${contextLabel(scope.context)}${staged ? `, from the next message, ${price}` : ''}`,
  );
  const items = $derived<MenuItem[]>([
    ...(options?.models ?? []).map(
      (entry): MenuItem => ({
        value: `model:${entry.id}`,
        label: `${entry.name}, $${entry.rates.output} per million out`,
        icon: entry.id === scope.model ? 'check' : undefined,
      }),
    ),
    ...(current?.efforts ?? []).map(
      (value): MenuItem => ({
        value: `effort:${value}`,
        label: `Effort ${value}`,
        icon: value === scope.effort ? 'check' : undefined,
        disabled: fixed && value !== scope.effort,
      }),
    ),
    ...contexts.map(
      (value): MenuItem => ({
        value: `context:${value}`,
        label: `Context ${contextLabel(value)}`,
        icon: value === scope.context ? 'check' : undefined,
      }),
    ),
  ]);

  function onSelect(value: string): void {
    const [kind, choice = ''] = value.split(':');
    if (kind === 'model') {
      const next = options?.models.find((entry) => entry.id === choice);
      const effort =
        next && !next.efforts.includes(scope.effort) ? (next.efforts.at(-1) ?? scope.effort) : scope.effort;
      onchange({ model: choice, effort, context: snapContext(contextChoices(options, next), scope.context) });
    } else if (kind === 'effort') {
      onchange({ ...scope, effort: choice });
    } else {
      onchange({ ...scope, context: Number(choice) });
    }
  }
</script>

<div class="scope-menu" class:scope-menu--staged={staged}>
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
  .scope-menu {
    display: flex;
    min-width: 0;
  }

  .scope-menu--staged :global(.btn) {
    box-shadow: inset 0 0 0 1px var(--honey-line);
  }
</style>
