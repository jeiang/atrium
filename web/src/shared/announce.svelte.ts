export type Politeness = 'polite' | 'assertive';

export const announcement = $state({ polite: '', assertive: '' });

export function announce(message: string, politeness: Politeness = 'polite'): void {
  announcement[politeness] = '';
  requestAnimationFrame(() => {
    announcement[politeness] = message;
  });
}
