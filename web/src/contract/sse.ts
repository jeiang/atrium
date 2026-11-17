export interface SseEvent {
  event: string;
  data: string;
  id?: string;
}

export interface SseHandlers {
  onEvent(event: SseEvent): void;
  onRetry?(ms: number): void;
}

const LINE = /\r\n|\r|\n/;

export async function readEventStream(
  body: ReadableStream<Uint8Array<ArrayBuffer>>,
  handlers: SseHandlers,
): Promise<void> {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  let event = '';
  let data: string[] = [];
  let id: string | undefined;

  const line = (text: string): void => {
    if (text === '') {
      if (data.length > 0) handlers.onEvent({ event: event || 'message', data: data.join('\n'), id });
      event = '';
      data = [];
      return;
    }
    if (text.startsWith(':')) return;
    const colon = text.indexOf(':');
    const field = colon < 0 ? text : text.slice(0, colon);
    let value = colon < 0 ? '' : text.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') event = value;
    else if (field === 'data') data.push(value);
    else if (field === 'id' && !value.includes('\0')) id = value;
    else if (field === 'retry' && /^\d+$/.test(value)) handlers.onRetry?.(Number(value));
  };

  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += value;
      for (;;) {
        const match = LINE.exec(buffer);
        if (!match) break;
        if (match[0] === '\r' && match.index === buffer.length - 1) break;
        line(buffer.slice(0, match.index));
        buffer = buffer.slice(match.index + match[0].length);
      }
    }
  } finally {
    reader.releaseLock();
  }
}
