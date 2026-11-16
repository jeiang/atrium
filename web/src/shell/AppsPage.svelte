<script lang="ts">
  import { apps } from '../apps';
</script>

<h1>Apps</h1>
<ul class="app-list">
  {#each apps as app (app.id)}
    <li>{app.title}</li>
  {/each}
</ul>
