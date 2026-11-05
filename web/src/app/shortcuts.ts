// Global keyboard shortcuts. Single keys never fire while typing; sequences
// ("g t") must complete within a second.

import { useEffect, useRef } from 'react';

export interface Shortcut {
  keys: string;
  label: string;
  group: string;
  run: () => void;
  when?: () => boolean;
}

export function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
}

function eventKey(event: KeyboardEvent): string {
  const mod = event.metaKey || event.ctrlKey ? 'mod+' : '';
  const shift = event.shiftKey && event.key.length > 1 ? 'shift+' : '';
  return mod + shift + (event.key.length === 1 ? event.key.toLowerCase() : event.key);
}

export function useShortcuts(shortcuts: Shortcut[]): void {
  useEffect(() => {
    let pending = '';
    let timer = 0;
    function onKey(event: KeyboardEvent) {
      if (event.defaultPrevented || event.isComposing) return;
      const key = eventKey(event);
      const typing = isTyping(event.target);
      if (typing && !key.startsWith('mod+')) return;
      if (document.querySelector('[role="dialog"], [role="alertdialog"]') && !key.startsWith('mod+')) return;
      const sequence = pending ? `${pending} ${key}` : key;
      const match = shortcuts.find(s => s.keys === sequence && (!s.when || s.when()));
      if (match) {
        event.preventDefault();
        pending = '';
        window.clearTimeout(timer);
        match.run();
        return;
      }
      if (shortcuts.some(s => s.keys.startsWith(sequence + ' '))) {
        pending = sequence;
        window.clearTimeout(timer);
        timer = window.setTimeout(() => (pending = ''), 1000);
        return;
      }
      pending = '';
    }
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      window.clearTimeout(timer);
    };
  }, [shortcuts]);
}

/**
 * Page-scoped single keys (list navigation, review keys). They follow the
 * global rules: never while typing, never under an open dialog, never with a
 * modifier. The handler returns true when it used the key.
 */
export function usePageKeys(handler: (key: string) => boolean | void): void {
  const latest = useRef(handler);
  latest.current = handler;
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.defaultPrevented || event.isComposing || event.metaKey || event.ctrlKey || event.altKey) return;
      if (isTyping(event.target) || document.querySelector('[role="dialog"], [role="alertdialog"]')) return;
      if (latest.current(event.key)) event.preventDefault();
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
}

export function formatKeys(keys: string): string[] {
  const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);
  return keys.split(' ').map(part =>
    part
      .replace('mod+', mac ? '⌘' : 'Ctrl+')
      .replace('shift+', '⇧')
      .replace('Escape', 'Esc')
      .replace('ArrowUp', '↑')
      .replace('ArrowDown', '↓')
      .replace('ArrowRight', '→')
      .replace('ArrowLeft', '←')
      .replace('Enter', '↵'),
  );
}
