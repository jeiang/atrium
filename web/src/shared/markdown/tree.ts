export type Inline =
  | { t: 'text'; v: string }
  | { t: 'code'; v: string }
  | { t: 'br' }
  | { t: 'strong' | 'em' | 'del'; c: Inline[] }
  | { t: 'link'; href: string; title: string | undefined; external: boolean; c: Inline[] };

export type Align = 'left' | 'center' | 'right' | null;

export type Block =
  | { t: 'p'; bare: boolean; c: Inline[] }
  | { t: 'raw'; v: string }
  | { t: 'h'; level: number; c: Inline[] }
  | { t: 'code'; lang: string; v: string }
  | { t: 'quote'; c: Block[] }
  | { t: 'list'; ordered: boolean; start: number; items: Block[][] }
  | { t: 'hr' }
  | { t: 'table'; align: Align[]; head: Inline[][]; rows: Inline[][][] };

export type Parse = (source: string) => Block[];
