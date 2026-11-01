// Client-only UI state shared across views: overlays and per-viewer
// preferences. Preferences persist in localStorage when it is available.

import { useSyncExternalStore } from 'react';
import type { DraftTask } from './actions';

export type Theme = 'system' | 'dark' | 'light';

/** The colors of the dark theme; styles/tokens.css defines each one. */
export const DARK_PALETTES = [
  { id: 'kasuri', label: 'Kasuri', chrome: '#120c24', surface: '#1b1433', accent: '#34d399' },
  { id: 'midnight', label: 'Midnight', chrome: '#0a0b0d', surface: '#15161a', accent: '#7f86ff' },
  { id: 'graphite', label: 'Graphite', chrome: '#111111', surface: '#1c1c1c', accent: '#e0e0e0' },
  { id: 'ocean', label: 'Ocean', chrome: '#071018', surface: '#0f1d29', accent: '#3fc1e8' },
  { id: 'forest', label: 'Forest', chrome: '#08100c', surface: '#121c17', accent: '#52c98a' },
  { id: 'plum', label: 'Plum', chrome: '#0f0a14', surface: '#1b1424', accent: '#d17ce8' },
  { id: 'ember', label: 'Ember', chrome: '#110c09', surface: '#1e1712', accent: '#f08a4b' },
  { id: 'oled', label: 'True black', chrome: '#000000', surface: '#0b0b0b', accent: '#8b93ff' },
] as const;

export type DarkPalette = (typeof DARK_PALETTES)[number]['id'];
export type DiffLayout = 'unified' | 'split';

export interface UIState {
  paletteOpen: boolean;
  paletteMode: 'all' | 'files' | 'commands';
  newTask: Partial<DraftTask> | null;
  shortcutsOpen: boolean;
  sidebarCollapsed: boolean;
  theme: Theme;
  darkPalette: DarkPalette;
  diffLayout: DiffLayout;
  wrapLines: boolean;
  /** Whether merging a worktree also removes it and its branch; one choice for every merge. */
  removeWorktreeOnMerge: boolean;
}

const PREFS_KEY = 'mini-orca.prefs.v2';

function loadPrefs(): Partial<UIState> {
  try {
    return JSON.parse(localStorage.getItem(PREFS_KEY) ?? '{}') as Partial<UIState>;
  } catch {
    return {};
  }
}

let state: UIState = {
  paletteOpen: false,
  paletteMode: 'all',
  newTask: null,
  shortcutsOpen: false,
  sidebarCollapsed: false,
  theme: 'system',
  darkPalette: 'kasuri',
  diffLayout: 'unified',
  wrapLines: false,
  removeWorktreeOnMerge: true,
  ...pick(loadPrefs()),
};

function pick(prefs: Partial<UIState>): Partial<UIState> {
  const out: Partial<UIState> = {};
  if (prefs.theme === 'dark' || prefs.theme === 'light' || prefs.theme === 'system') out.theme = prefs.theme;
  if (DARK_PALETTES.some(palette => palette.id === prefs.darkPalette)) out.darkPalette = prefs.darkPalette;
  if (prefs.diffLayout === 'split' || prefs.diffLayout === 'unified') out.diffLayout = prefs.diffLayout;
  if (typeof prefs.wrapLines === 'boolean') out.wrapLines = prefs.wrapLines;
  if (typeof prefs.sidebarCollapsed === 'boolean') out.sidebarCollapsed = prefs.sidebarCollapsed;
  if (typeof prefs.removeWorktreeOnMerge === 'boolean') out.removeWorktreeOnMerge = prefs.removeWorktreeOnMerge;
  return out;
}

const listeners = new Set<() => void>();

export function getUI(): UIState {
  return state;
}

export function setUI(patch: Partial<UIState>): void {
  state = { ...state, ...patch };
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify({ theme: state.theme, darkPalette: state.darkPalette, diffLayout: state.diffLayout, wrapLines: state.wrapLines, sidebarCollapsed: state.sidebarCollapsed, removeWorktreeOnMerge: state.removeWorktreeOnMerge }));
  } catch {
    // Preferences are a convenience; the UI works without storage.
  }
  listeners.forEach(fn => fn());
}

export function useUI<T>(select: (state: UIState) => T): T {
  return useSyncExternalStore(
    fn => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => select(state),
  );
}

export function openNewTask(prefill: Partial<DraftTask> = {}): void {
  setUI({ newTask: prefill, paletteOpen: false });
}

export function openPalette(mode: UIState['paletteMode'] = 'all'): void {
  setUI({ paletteOpen: true, paletteMode: mode });
}
