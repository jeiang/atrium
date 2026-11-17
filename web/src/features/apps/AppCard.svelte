<script lang="ts">
  import type { AppEntry, Binding } from '../../contract';
  import Badge from '../../shared/ui/Badge.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import Switch from '../../shared/ui/Switch.svelte';
  import { navLinks, repairHref, stateOf, unmetNeeds } from '../../shell/manifest';
  import ConnectionState from '../connections/ConnectionState.svelte';
  import { slotLabel } from '../connections/connections';
  import { names, type Outcome, unmetNames } from './apps';

  interface Props {
    app: AppEntry;
    titled?: boolean;
    links?: boolean;
    level?: 2 | 3;
    pending?: boolean;
    target?: boolean;
    outcome?: Outcome;
    onswitch?: (next: boolean) => void;
  }

  let { app, titled = true, links = false, level = 3, pending = false, target, outcome, onswitch }: Props = $props();

  const state = $derived(stateOf(app));
  const blocked = $derived(app.enabled ? [] : unmetNames(app));
  const locked = $derived(
    !app.switchable ? 'Set by the host' : blocked.length > 0 ? `Turn on ${names(blocked)} first` : undefined,
  );
  const shownNeeds = $derived(links ? unmetNeeds(app) : app.needs);
  const pages = $derived(links ? navLinks(app) : []);
  const open = $derived(outcome && outcome.open.length > 0 ? outcome.open : onswitch ? blocked : []);

  function needLabel(need: Binding): string {
    return slotLabel(need.capability);
  }
</script>

<Card title={titled && !onswitch ? app.name : undefined} {level}>
  {#if onswitch}
    <Switch
      label={app.name}
      checked={pending && target !== undefined ? target : app.enabled}
      {pending}
      {locked}
      onchange={onswitch}
    />
  {/if}
  {#if app.enabled && app.state !== 'enabled'}
    <p class="app-card__state">
      <StatusMark tone={state.tone}>{state.label}</StatusMark>
      {#if app.reason}
        <span class="meta">{app.reason}</span>
      {/if}
    </p>
  {/if}
  {#if outcome}
    <Notice tone={outcome.tone} variant="inline" title={outcome.title}>{outcome.text}</Notice>
  {/if}
  {#if open.length > 0}
    <div class="cluster app-card__open">
      {#each open as item (item.id)}
        <a class="link" href="#app-{item.id}">Open {item.name}</a>
      {/each}
    </div>
  {/if}
  {#if shownNeeds.length > 0}
    <ul class="app-needs" aria-label="{app.name} needs">
      {#each shownNeeds as need (`${need.kind}/${need.capability}`)}
        <li class="app-need">
          <span class="app-need__name">{needLabel(need)}</span>
          {#if need.connection}
            <span class="meta">{need.connection}</span>
          {/if}
          {#if need.soft}
            <Badge>Optional</Badge>
          {/if}
          {#if need.met}
            <StatusMark tone="ok">Ready</StatusMark>
          {:else}
            <ConnectionState state={need.state} />
            {#if need.reason}
              <span class="meta app-need__reason">{need.reason}</span>
            {/if}
            <a class="link" href={repairHref(need)}
              >Repair<span class="visually-hidden"> {needLabel(need)} for {app.name}</span></a
            >
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#if !links && app.depends.length > 0}
    <p class="meta">Depends on {names(app.depends)}</p>
  {/if}
  {#if pages.length > 0}
    <ul class="app-links" aria-label="{app.name} pages">
      {#each pages as page (page.href)}
        <li>
          <a class="link app-link" href={page.href}
            >{page.label}
            {#if page.leaves}
              <Icon name="external" /><span class="visually-hidden">, opens the current interface</span>
            {/if}</a
          >
        </li>
      {/each}
    </ul>
  {/if}
</Card>
