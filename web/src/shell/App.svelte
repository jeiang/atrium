<script lang="ts">
  import { QueryClientProvider } from '@tanstack/svelte-query';
  import { queryClient } from '../shared/query';
  import Shell from './Shell.svelte';
</script>

<QueryClientProvider client={queryClient}>
  <Shell />
</QueryClientProvider>
