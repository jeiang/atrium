<script lang="ts">
  import ConnectionState from '../../features/connections/ConnectionState.svelte';
  import SecretField from '../../features/connections/SecretField.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import TextField from '../../shared/ui/TextField.svelte';

  const grace = new Date(Date.now() + 4 * 3600_000).toISOString();
  const states = ['ok', 'failing', 'untested', 'disabled', 'unconfigured', 'expiring', 'needs_reauth'];
  let secret = $state('');
</script>

<Section id="g-connections" data-gallery="connections" title="Connections">
  <div class="cluster">
    {#each states as state (state)}
      <ConnectionState {state} />
    {/each}
    <ConnectionState state="failing" graceUntil={grace} />
  </div>
  <div class="grid">
    <TextField label="Address" value="https://parcels.example.test/v1" readonly help="Set by the host" />
    <SecretField
      label="API key"
      bind:value={secret}
      help="Stored 12 Oct 2026 by operator@example.test. A new value replaces it."
    />
  </div>
</Section>
