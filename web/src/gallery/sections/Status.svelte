<script lang="ts">
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
</script>

<Section id="g-badges" data-gallery="badges" title="Badges and status">
  <div class="cluster">
    <Badge>Draft</Badge>
    <Badge tone="accent">Running</Badge>
    <Badge tone="ok">Sent</Badge>
    <Badge tone="warn">Needs you</Badge>
    <Badge tone="error">Failed</Badge>
    <Badge tone="info">Held</Badge>
  </div>
  <div class="cluster">
    <StatusMark tone="ok">Connected</StatusMark>
    <StatusMark tone="accent">Syncing</StatusMark>
    <StatusMark tone="warn">Token expires soon</StatusMark>
    <StatusMark tone="error">Offline</StatusMark>
    <StatusMark tone="info">Paused</StatusMark>
    <StatusMark>Idle</StatusMark>
  </div>
</Section>

<Section id="g-notices" data-gallery="notices" title="Notices">
  <Notice tone="warn" title="Mail sync is paused">
    The mail account needs a new password.
    {#snippet action()}
      <Button size="sm">Reconnect</Button>
    {/snippet}
  </Notice>
  <Notice tone="error" title="The digest did not send">The mail server refused the message.</Notice>
  <Notice tone="ok">All 4 reminders went out.</Notice>
  <Notice tone="info" variant="inline">Changes here apply to new threads only.</Notice>
</Section>
