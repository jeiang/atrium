<script lang="ts">
  const date = new Date().toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' });
</script>

<h1>Today</h1>
<p>{date}</p>
