import type { Attachment } from 'svelte/attachments';

const native = CSS.supports('anchor-name: --a') && CSS.supports('position-try-fallbacks: flip-block');
const gap = 4;
const edge = 8;
let count = 0;

function place(popup: HTMLElement, anchor: HTMLElement, matchWidth: boolean): void {
  if (!popup.matches(':popover-open')) return;
  const a = anchor.getBoundingClientRect();
  const width = document.documentElement.clientWidth;
  const height = document.documentElement.clientHeight;
  popup.style.inset = 'auto';
  popup.style.margin = '0';
  if (matchWidth) popup.style.inlineSize = `${a.width}px`;
  const p = popup.getBoundingClientRect();
  const below = a.bottom + gap;
  const above = a.top - gap - p.height;
  let top = below;
  if (below + p.height > height - edge) {
    top = above >= edge ? above : Math.max(edge, Math.min(below, height - edge - p.height));
  }
  let left = a.left;
  if (left + p.width > width - edge) left = a.right - p.width;
  left = Math.max(edge, Math.min(left, width - edge - p.width));
  popup.style.top = `${top}px`;
  popup.style.left = `${left}px`;
}

export function anchorTo(anchor: () => HTMLElement | undefined, matchWidth = false): Attachment<HTMLElement> {
  return (popup) => {
    const target = anchor();
    if (!target) return;
    if (native) {
      const name = `--anchor-${++count}`;
      target.style.setProperty('anchor-name', name);
      popup.style.setProperty('position-anchor', name);
      popup.classList.add('pop--anchored');
      return () => target.style.removeProperty('anchor-name');
    }
    const update = (): void => place(popup, target, matchWidth);
    const onToggle = (event: Event): void => {
      if ((event as ToggleEvent).newState === 'open') requestAnimationFrame(update);
    };
    popup.addEventListener('beforetoggle', onToggle);
    window.addEventListener('resize', update);
    window.addEventListener('scroll', update, { capture: true, passive: true });
    return () => {
      popup.removeEventListener('beforetoggle', onToggle);
      window.removeEventListener('resize', update);
      window.removeEventListener('scroll', update, { capture: true });
    };
  };
}
