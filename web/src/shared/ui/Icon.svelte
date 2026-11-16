<script lang="ts" module>
  const paths = {
    check: 'M5 12.5l4.5 4.5L19 7.5',
    close: 'M6 6l12 12M18 6L6 18',
    alert: 'M12 4.5l8.5 15h-17zM12 10v4M12 16.8v.2',
    info: 'M12 3.5a8.5 8.5 0 1 0 0 17a8.5 8.5 0 1 0 0-17M12 11v5M12 7.8v.2',
    error: 'M12 3.5a8.5 8.5 0 1 0 0 17a8.5 8.5 0 1 0 0-17M9 9l6 6M15 9l-6 6',
    chevron: 'M6 9l6 6 6-6',
    sort: 'M8 10l4-4 4 4M8 14l4 4 4-4',
    plus: 'M12 5v14M5 12h14',
    inbox: 'M4 13l2.5-7h11l2.5 7v5H4zM4 13h5l1 2h4l1-2h5',
  } as const;

  export type IconName = keyof typeof paths;
</script>

<script lang="ts">
  import './kit.css';

  let { name }: { name: IconName } = $props();
</script>

<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d={paths[name]} /></svg>
