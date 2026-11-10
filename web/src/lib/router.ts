// A small hash router. The daemon serves one document at /ui, so routes live
// in the fragment: #/tasks/<id>/files?file=internal/x.go. The selected
// checkout (`wt`) is a global parameter that survives navigation.

import { useSyncExternalStore } from 'react';

export interface Route {
  path: string;
  segments: string[];
  params: URLSearchParams;
}

export function parseHash(hash: string): Route {
  const raw = hash.replace(/^#/, '') || '/';
  const [pathPart, search = ''] = raw.split('?');
  const path = '/' + pathPart.split('/').filter(Boolean).map(decodeURIComponent).join('/');
  return { path, segments: path.split('/').filter(Boolean), params: new URLSearchParams(search) };
}

export function buildHash(path: string, params: Record<string, string | undefined> = {}): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) if (value) q.set(key, value);
  const encoded = '/' + path.split('/').filter(Boolean).map(encodeURIComponent).join('/');
  const search = q.toString();
  return '#' + encoded + (search ? '?' + search : '');
}

let cached: { hash: string; route: Route } | null = null;
const listeners = new Set<() => void>();

function notify(): void {
  for (const listener of listeners) listener();
}

function current(): Route {
  const hash = window.location.hash;
  if (!cached || cached.hash !== hash) cached = { hash, route: parseHash(hash) };
  return cached.route;
}

function subscribe(callback: () => void): () => void {
  if (listeners.size === 0) window.addEventListener('hashchange', notify);
  listeners.add(callback);
  return () => {
    listeners.delete(callback);
    if (listeners.size === 0) window.removeEventListener('hashchange', notify);
  };
}

/** The route now, for code outside React rendering. */
export function currentRoute(): Route {
  return current();
}

export function useRoute(): Route {
  return useSyncExternalStore(subscribe, current, current);
}

/** Navigates to `path`, keeping the selected checkout unless overridden. */
export function navigate(path: string, params: Record<string, string | undefined> = {}, options: { replace?: boolean } = {}): void {
  const wt = 'wt' in params ? params.wt : current().params.get('wt') ?? undefined;
  const hash = buildHash(path, { ...params, wt });
  if (hash === window.location.hash) return;
  if (options.replace) window.history.replaceState(null, '', window.location.pathname + window.location.search + hash);
  else window.location.hash = hash;
  notify();
}

export function setParam(key: string, value: string | undefined, options: { replace?: boolean } = { replace: true }): void {
  const route = current();
  const params: Record<string, string | undefined> = Object.fromEntries(route.params.entries());
  params[key] = value;
  navigate(route.path, params, options);
}
