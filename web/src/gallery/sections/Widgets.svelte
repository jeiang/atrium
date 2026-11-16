<script lang="ts">
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Combobox from '../../shared/ui/Combobox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import Listbox, { type ListOption } from '../../shared/ui/Listbox.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Menu, { type MenuItem } from '../../shared/ui/Menu.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Tabs, { type TabItem } from '../../shared/ui/Tabs.svelte';
  import { threads } from '../fixtures';

  const threadActions: readonly MenuItem[] = [
    { value: 'rename', label: 'Rename' },
    { value: 'pin', label: 'Pin' },
    { value: 'mute', label: 'Mute for a day' },
    { value: 'export', label: 'Export', disabled: true },
    { value: 'delete', label: 'Delete', danger: true },
  ];

  const rowActions: readonly MenuItem[] = [
    { value: 'open', label: 'Open' },
    { value: 'mark', label: 'Mark as read' },
    { value: 'archive', label: 'Archive' },
  ];

  const folders: readonly ListOption[] = [
    { value: 'inbox', label: 'Inbox' },
    { value: 'receipts', label: 'Receipts', detail: '214 messages' },
    { value: 'travel', label: 'Travel', detail: '38 messages' },
    { value: 'garden', label: 'Garden club', detail: '61 messages' },
    { value: 'bills', label: 'Bills', detail: '97 messages' },
    { value: 'school', label: 'School', detail: '12 messages' },
    { value: 'archive', label: 'Archive', disabled: true },
    { value: 'newsletters', label: 'Newsletters', detail: '403 messages' },
    { value: 'family', label: 'Family', detail: '55 messages' },
    { value: 'health', label: 'Health', detail: '19 messages' },
    { value: 'home', label: 'Home repairs', detail: '27 messages' },
    { value: 'books', label: 'Book club', detail: '44 messages' },
  ];

  const contacts: readonly ListOption[] = [
    { value: 'c1', label: 'Robin Marsh', detail: 'robin@example.test' },
    { value: 'c2', label: 'Hillfield Library', detail: 'holds@library.example.test' },
    { value: 'c3', label: 'Riverside Dental', detail: 'desk@dental.example.test' },
    { value: 'c4', label: 'Rowan Ellery', detail: 'rowan@example.test' },
    { value: 'c5', label: 'Cedar & Pine Supply', detail: 'orders@cedar.example.test' },
    { value: 'c6', label: 'Marta Quill', detail: 'marta@example.test' },
  ];

  const topics: readonly ListOption[] = [
    { value: 'decisions', label: 'Mail decisions' },
    { value: 'calendar', label: 'Calendar changes' },
    { value: 'downloads', label: 'Finished downloads' },
    { value: 'health', label: 'Server health' },
  ];

  const knowledgeTabs: readonly TabItem[] = [
    { value: 'page', label: 'Page' },
    { value: 'facts', label: 'Facts' },
    { value: 'review', label: 'Review' },
    { value: 'about', label: 'About me' },
  ];

  const settingsTabs: readonly TabItem[] = [
    { value: 'general', label: 'General' },
    { value: 'mail', label: 'Mail' },
    { value: 'calendar', label: 'Calendar' },
    { value: 'knowledge', label: 'Knowledge' },
    { value: 'library', label: 'Library' },
    { value: 'health', label: 'Health' },
    { value: 'apps', label: 'Apps' },
    { value: 'backups', label: 'Backups', disabled: true },
  ];

  const panelText: Record<string, string> = {
    page: 'Allotment plot 14: raised beds on the south side, water butt by the gate.',
    facts: 'Twelve facts mention the allotment; two wait for confirmation.',
    review: 'Three changes from last night are ready to review.',
    about: 'Prefers morning reminders and short summaries.',
  };

  let menuResult = $state('Nothing chosen');
  let folder = $state<ListOption>();
  let contact = $state<ListOption>();
  let knowledge = $state('page');
  let settings = $state('general');
  let defaultFolder = $state<string>('receipts');
  let notify = $state<string[]>(['decisions']);
  let moveOpen = $state(false);
  let moveTo = $state<string>();

  function searchContacts(query: string, signal: AbortSignal): Promise<readonly ListOption[]> {
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        if (query.toLowerCase() === 'offline') reject(new Error('offline'));
        else resolve(contacts.filter((c) => `${c.label} ${c.detail}`.toLowerCase().includes(query.toLowerCase())));
      }, 400);
      signal.addEventListener('abort', () => {
        window.clearTimeout(timer);
        reject(signal.reason);
      });
    });
  }

  function onMove(value: string): void {
    moveTo = value;
    moveOpen = false;
  }
</script>

<Section id="g-menus" data-gallery="menus" title="Menus">
  <div class="cluster">
    <Menu label="Thread actions" items={threadActions} onselect={(value) => (menuResult = `Chose ${value}`)} />
    <p class="meta" data-testid="menu-result">{menuResult}</p>
  </div>
  <div class="grid">
    <List label="Threads with menus">
      {#each threads as thread (thread.id)}
        <ListRow title={thread.title} meta={thread.meta} onclick={() => (menuResult = `Opened ${thread.title}`)}>
          {#snippet end()}
            <Menu
              label="More for {thread.title}"
              icon="more"
              iconOnly
              variant="quiet"
              items={rowActions}
              onselect={(value) => (menuResult = `Chose ${value} for ${thread.title}`)}
            />
          {/snippet}
        </ListRow>
      {/each}
    </List>
    <Card title="Shed order">
      <p>Twelve cedar boards and one box of brass screws.</p>
      {#snippet actions()}
        <Button variant="primary">Review</Button>
        <Menu
          label="More for the shed order"
          icon="more"
          iconOnly
          variant="quiet"
          items={rowActions}
          onselect={(value) => (menuResult = `Chose ${value} for the shed order`)}
        />
      {/snippet}
    </Card>
  </div>
</Section>

<Section id="g-comboboxes" data-gallery="comboboxes" title="Comboboxes">
  <div class="grid">
    <Combobox label="Folder" placeholder="Type a folder name" options={folders} bind:selected={folder} />
    <Combobox label="Contact" placeholder="Search by name or address" search={searchContacts} bind:selected={contact} />
  </div>
  <p class="meta" data-testid="combo-result">Folder: {folder?.label ?? 'none'}. Contact: {contact?.label ?? 'none'}.</p>
</Section>

<Section id="g-tabs" data-gallery="tabs" title="Tabs">
  <Tabs label="Knowledge" tabs={knowledgeTabs} bind:value={knowledge}>
    {#snippet panel(
    value,
  )}
      <p>{panelText[value]}</p>
      <Button size="sm">Open {value}</Button>
    {/snippet}
  </Tabs>
  <Tabs label="Settings sections" tabs={settingsTabs} bind:value={settings}>
    {#snippet panel(
    value,
  )}
      <p>Settings for {value}.</p>
    {/snippet}
  </Tabs>
</Section>

<Section id="g-listboxes" data-gallery="listboxes" title="Listboxes">
  <div class="grid">
    <Listbox label="Default folder" options={folders.slice(0, 7)} bind:value={defaultFolder} />
    <Listbox label="Notify me about" options={topics} multiple bind:values={notify} />
  </div>
  <div class="cluster">
    <Button onclick={() => (moveOpen = true)}>Move to folder</Button>
    <p class="meta" data-testid="listbox-result">
      Default {defaultFolder}. Notify {notify.join(', ') || 'none'}. Moved to {moveTo ?? 'nowhere'}.
    </p>
  </div>
</Section>

<Dialog bind:open={moveOpen} title="Move to folder" sheet>
  <Listbox label="Folder" labelHidden options={folders} value={moveTo} onchange={onMove} />
</Dialog>
