<script lang="ts">
  import Page from '../shared/ui/Page.svelte';
  import Segmented from '../shared/ui/Segmented.svelte';
  import { schemeOptions } from './fixtures';
  import Actions from './sections/Actions.svelte';
  import Approvals from './sections/Approvals.svelte';
  import Apps from './sections/Apps.svelte';
  import Assistant from './sections/Assistant.svelte';
  import AssistantSettings from './sections/AssistantSettings.svelte';
  import Calendar from './sections/Calendar.svelte';
  import Collections from './sections/Collections.svelte';
  import Connections from './sections/Connections.svelte';
  import Forms from './sections/Forms.svelte';
  import Foundations from './sections/Foundations.svelte';
  import Knowledge from './sections/Knowledge.svelte';
  import Overlays from './sections/Overlays.svelte';
  import Prose from './sections/Prose.svelte';
  import Settings from './sections/Settings.svelte';
  import Simulator from './sections/Simulator.svelte';
  import States from './sections/States.svelte';
  import Status from './sections/Status.svelte';
  import Tools from './sections/Tools.svelte';
  import Widgets from './sections/Widgets.svelte';

  let scheme = $state<string>(document.documentElement.dataset.theme ?? 'auto');

  $effect(() => {
    const root = document.documentElement;
    if (scheme === 'auto') delete root.dataset.theme;
    else root.dataset.theme = scheme;
  });
</script>

<Page title="Component gallery" eyebrow="Mock only" lede="Every base surface in each state, against mock data.">
  {#snippet actions()}
    <Segmented label="Color scheme" options={schemeOptions} bind:value={scheme} />
  {/snippet}
  <Foundations />
  <Actions />
  <Forms />
  <Status />
  <Collections />
  <States />
  <Settings />
  <Overlays />
  <Connections />
  <Apps />
  <Calendar />
  <Widgets />
  <Approvals />
  <Tools />
  <Assistant />
  <AssistantSettings />
  <Prose />
  <Knowledge />
  <Simulator />
</Page>
