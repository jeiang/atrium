export function typeahead(): (key: string, labels: readonly string[], from: number) => number {
  let buffer = '';
  let last = 0;
  return (key, labels, from) => {
    const now = performance.now();
    buffer = now - last > 500 ? key : buffer + key;
    last = now;
    const query = buffer.toLowerCase();
    const repeat = [...query].every((char) => char === query[0]);
    const start = buffer.length === 1 || repeat ? from + 1 : from;
    const needle = repeat ? query.charAt(0) : query;
    for (let step = 0; step < labels.length; step++) {
      const index = (start + step) % labels.length;
      if (labels[index]?.toLowerCase().startsWith(needle)) return index;
    }
    return -1;
  };
}

export function isTypeaheadKey(event: KeyboardEvent): boolean {
  return event.key.length === 1 && event.key !== ' ' && !event.ctrlKey && !event.metaKey && !event.altKey;
}
