import { afterEach, describe, expect, it, vi } from 'vitest';

// The store reads saved preferences once, when it is first imported.
async function freshUI() {
  vi.resetModules();
  const store = await import('./store');
  return store.getUI();
}

describe('preferences saved under earlier names', () => {
  afterEach(() => localStorage.clear());

  it('carries Kasuri preferences over with the palette renamed', async () => {
    localStorage.setItem('kasuri.prefs.v2', JSON.stringify({ theme: 'light', darkPalette: 'kasuri', wrapLines: true }));
    const ui = await freshUI();
    expect(ui.theme).toBe('light');
    expect(ui.darkPalette).toBe('kogin');
    expect(ui.wrapLines).toBe(true);
  });

  it('prefers Kasuri preferences over Mini-Orca ones', async () => {
    localStorage.setItem('mini-orca.prefs.v2', JSON.stringify({ theme: 'dark' }));
    localStorage.setItem('kasuri.prefs.v2', JSON.stringify({ theme: 'light' }));
    expect((await freshUI()).theme).toBe('light');
  });

  it('carries Mini-Orca preferences over without the dark palette', async () => {
    localStorage.setItem('mini-orca.prefs.v2', JSON.stringify({ theme: 'dark', darkPalette: 'midnight' }));
    const ui = await freshUI();
    expect(ui.theme).toBe('dark');
    expect(ui.darkPalette).toBe('kogin');
  });

  it('uses Kogin preferences once they exist', async () => {
    localStorage.setItem('kasuri.prefs.v2', JSON.stringify({ theme: 'light' }));
    localStorage.setItem('kogin.prefs.v2', JSON.stringify({ theme: 'dark', darkPalette: 'midnight' }));
    const ui = await freshUI();
    expect(ui.theme).toBe('dark');
    expect(ui.darkPalette).toBe('midnight');
  });
});

describe('density and font', () => {
  afterEach(() => localStorage.clear());

  it('read comfortable and the bundled Inter face until chosen otherwise', async () => {
    const ui = await freshUI();
    expect(ui.density).toBe('comfortable');
    expect(ui.font).toBe('inter');
  });

  it('keep the saved choice and drop one the UI does not know', async () => {
    localStorage.setItem('kogin.prefs.v2', JSON.stringify({ density: 'compact', font: 'papyrus' }));
    const ui = await freshUI();
    expect(ui.density).toBe('compact');
    expect(ui.font).toBe('inter');
  });

  it('are saved with the other preferences', async () => {
    vi.resetModules();
    const store = await import('./store');
    store.setUI({ density: 'compact', font: 'system' });
    const saved = JSON.parse(localStorage.getItem('kogin.prefs.v2') ?? '{}') as Record<string, unknown>;
    expect(saved.density).toBe('compact');
    expect(saved.font).toBe('system');
  });
});
