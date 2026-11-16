export interface Frame {
  id?: string;
  event: string;
  data: string;
}

function parse(block: string): Frame | undefined {
  const fields = new Map<string, string>();
  for (const line of block.split('\n')) {
    const colon = line.indexOf(':');
    if (colon <= 0) continue;
    fields.set(line.slice(0, colon), line.slice(colon + 1).trimStart());
  }
  const event = fields.get('event');
  if (!event) return undefined;
  return { id: fields.get('id'), event, data: fields.get('data') ?? '' };
}

export async function readStream(url: string, signal: AbortSignal, onFrame: (frame: Frame) => void): Promise<number> {
  const response = await fetch(url, { headers: { Accept: 'text/event-stream' }, signal });
  if (!response.ok || !response.body) return response.status;
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += value;
    let end = buffer.indexOf('\n\n');
    while (end >= 0) {
      const frame = parse(buffer.slice(0, end));
      buffer = buffer.slice(end + 2);
      if (frame) onFrame(frame);
      end = buffer.indexOf('\n\n');
    }
  }
  return response.status;
}
