<script lang="ts">
  import { useAbout } from '../../shared/settings/settings';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const about = useAbout();
  const data = $derived(about.data);
</script>

<section class="settings-about" aria-labelledby="about-title">
  <h3 class="settings-about__title" id="about-title">About</h3>
  {#if data}
    <dl class="settings-about__list">
      <div class="setting__readonly">
        <dt class="setting__label">Version</dt>
        <dd class="setting__value">{data.version}</dd>
      </div>
      {#if data.build}
        <div class="setting__readonly">
          <dt class="setting__label">Build</dt>
          <dd class="setting__value">{data.build}</dd>
        </div>
      {/if}
      <div class="setting__readonly">
        <dt class="setting__label">Go</dt>
        <dd class="setting__value">{data.go_version}</dd>
      </div>
      <div class="setting__readonly">
        <dt class="setting__label">API version</dt>
        <dd class="setting__value">{data.api_version}</dd>
      </div>
      <div class="setting__readonly">
        <dt class="setting__label">Database schema</dt>
        <dd class="setting__value">{data.schema_version}</dd>
      </div>
    </dl>
  {:else if about.isError}
    <ViewState kind="error" title="atrium could not say which version runs" level={4}>
      <button class="link" type="button" onclick={() => void about.refetch()}>Try again</button>
    </ViewState>
  {:else}
    <Skeleton lines={3} label="Loading the version" />
  {/if}
  <a class="link settings-about__notices" href="/third-party-notices.txt" target="_self">Third-party notices</a>
</section>
