<script lang="ts">
  import Button from '../ui/Button.svelte';
  import './assistant.css';

  interface Props {
    value?: string;
    disabled?: boolean;
    busy?: boolean;
    placeholder?: string;
    onsubmit: (text: string) => void;
  }

  let { value = $bindable(''), disabled = false, busy = false, placeholder = 'Ask atrium', onsubmit }: Props = $props();

  const uid = $props.id();
  const empty = $derived(value.trim() === '');

  function submit(): void {
    if (disabled || busy || empty) return;
    onsubmit(value.trim());
  }

  function onSubmit(event: SubmitEvent): void {
    event.preventDefault();
    submit();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' || event.shiftKey || event.isComposing || event.keyCode === 229) return;
    event.preventDefault();
    submit();
  }
</script>

<form class="composer" aria-busy={busy || undefined} onsubmit={onSubmit}>
  <label class="visually-hidden" for="{uid}-text">Message</label>
  <textarea
    class="composer__text"
    id="{uid}-text"
    rows="1"
    {placeholder}
    {disabled}
    enterkeyhint="send"
    bind:value
    onkeydown={onKeydown}
  ></textarea>
  <Button
    type="submit"
    variant="primary"
    icon="arrow"
    label="Send"
    pending={busy}
    disabled={disabled || empty}
    class="composer__send"
  />
</form>
