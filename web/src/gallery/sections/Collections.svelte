<script lang="ts">
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import DataTable, { type Column, type Sort } from '../../shared/ui/DataTable.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import { type MailRow, mailRows, threads } from '../fixtures';

  const columns: readonly Column[] = [
    { key: 'sender', label: 'From', sortable: true },
    { key: 'subject', label: 'Subject' },
    { key: 'received', label: 'Received', sortable: true },
    { key: 'size', label: 'Size (KB)', numeric: true, sortable: true },
  ];

  let sort = $state<Sort>({ key: 'received', direction: 'descending' });
  let current = $state<string>();
  let opened = $state('');
  let selectedThread = $state('t1');

  const sorted = $derived(
    [...mailRows].sort((a, b) => {
      const key = sort.key as keyof MailRow;
      const order = a[key] < b[key] ? -1 : a[key] > b[key] ? 1 : 0;
      return sort.direction === 'ascending' ? order : -order;
    }),
  );
</script>

<Section id="g-cards" data-gallery="cards" title="Cards and lists">
  <div class="grid">
    <Card title="Shed order">
      {#snippet meta()}
        <Badge tone="warn">Needs you</Badge>
      {/snippet}
      <p>Twelve cedar boards and one box of brass screws from Cedar & Pine Supply.</p>
      {#snippet actions()}
        <Button variant="primary">Review</Button>
        <Button variant="quiet">Later</Button>
      {/snippet}
    </Card>
    <Card title="Weather card" selected>
      <p>Selected card.</p>
    </Card>
    <Card title="Library hold" pending>
      <p>Refreshing.</p>
    </Card>
  </div>
  <List label="Threads">
    {#each threads as thread (thread.id)}
      <ListRow
        title={thread.title}
        meta={thread.meta}
        unread={thread.unread}
        selected={selectedThread === thread.id}
        onclick={() => (selectedThread = thread.id)}
      >
        {#snippet end()}
          <Button size="sm" variant="quiet" icon="close" label="Archive {thread.title}" />
        {/snippet}
      </ListRow>
    {/each}
    <ListRow title="Syncing the allotment calendar" meta="Pending" pending />
  </List>
</Section>

<Section id="g-table" data-gallery="table" title="Data table" meta="Inbox and Settings only">
  <DataTable
    caption="Inbox"
    {columns}
    rows={sorted}
    key={(row) => row.id}
    bind:sort
    bind:current
    onopen={(row) => (opened = row.subject)}
  >
    {#snippet cell(
    row,
    column,
  )}
      {row[column.key as keyof MailRow]}
    {/snippet}
  </DataTable>
  <p class="meta" data-testid="opened">{opened ? `Opened ${opened}` : 'Nothing opened'}</p>
  <DataTable caption="Empty inbox" {columns} rows={[]} key={(row: MailRow) => row.id} empty="No mail">
    {#snippet cell(
    row,
    column,
  )}
      {row[column.key as keyof MailRow]}
    {/snippet}
  </DataTable>
</Section>
