import { describe, expect, it } from 'vitest';
import { architectureLines, componentOf, docTextParts, docsSection, firstSentence, symbolRefs, walkthroughsThrough } from './docs';

const components = [{ path: '.' }, { path: 'cmd' }, { path: 'internal' }, { path: 'internal/utils' }];

describe('docs helpers', () => {
  it('puts a file in the deepest component around it', () => {
    expect(componentOf('main.go', components)).toBe('.');
    expect(componentOf('cmd/add.go', components)).toBe('cmd');
    expect(componentOf('internal/utils/crypto.go', components)).toBe('internal/utils');
    expect(componentOf('internal/daemon/daemon.go', components)).toBe('internal');
    expect(componentOf('internal/utilsx/a.go', components)).toBe('internal');
    expect(componentOf('x.go', [{ path: 'cmd' }])).toBeUndefined();
  });

  it('finds the walkthroughs that pass through a component', () => {
    const walkthroughs = [
      { title: 'Add', summary: '', steps: [{ text: 'a', path: 'cmd/add.go' }, { text: 'b', path: 'internal/utils/io.go' }] },
      { title: 'Daemon', summary: '', steps: [{ text: 'c', path: 'internal/daemon/d.go' }] },
    ];
    expect(walkthroughsThrough('internal/utils', walkthroughs, components).map(w => w.n)).toEqual([1]);
    expect(walkthroughsThrough('internal', walkthroughs, components).map(w => w.n)).toEqual([2]);
    expect(walkthroughsThrough('.', walkthroughs, components)).toEqual([]);
  });

  it('keeps the first sentence and cuts long ones at a word', () => {
    expect(firstSentence('Saves items. Then more.')).toBe('Saves items.');
    expect(firstSentence('Uses v1.2 of it.  Next.')).toBe('Uses v1.2 of it.');
    expect(firstSentence('one two three four five', 12)).toBe('one two…');
    expect(firstSentence('Parses config files (e.g. YAML and TOML) and checks them. More.')).toBe('Parses config files (e.g. YAML and TOML) and checks them.');
    expect(firstSentence('Runs in the U.S. region. More.')).toBe('Runs in the U.S. region.');
    expect(firstSentence('Reads config.yaml. More.')).toBe('Reads config.yaml.');
    expect(firstSentence(undefined)).toBe('');
    expect(firstSentence('Runs `execAdd`. More.')).toBe('Runs execAdd.');
  });

  it('sends older links to the page that replaced theirs', () => {
    expect(docsSection('runbook')).toBe('start');
    expect(docsSection('scenarios')).toBe('flows');
    expect(docsSection('glossary')).toBe('overview');
    expect(docsSection(undefined)).toBe('overview');
    expect(docsSection('components')).toBe('components');
    expect(docsSection('changes')).toBe('changes');
  });

  const refs = {
    files: new Set(['cmd/add.go', 'internal/utils/io.go']),
    components: new Set(['cmd', 'internal/utils']),
    symbols: symbolRefs([
      { title: 'Add', summary: '', steps: [{ text: 'a', path: 'cmd/add.go', symbol: 'execAdd', line: 12 }, { text: 'b', path: 'internal/utils/io.go', symbol: 'Store.Save', line: 30 }] },
      { title: 'Get', summary: '', steps: [{ text: 'c', path: 'cmd/get.go', symbol: 'run', line: 3 }, { text: 'd', path: 'cmd/add.go', symbol: 'run', line: 40 }, { text: 'e', path: 'cmd/add.go' }] },
    ]),
  };

  it('links backticked names and file paths in prose to the code', () => {
    expect(docTextParts('`execAdd` in cmd/add.go saves via `Store.Save()`.', refs)).toEqual([
      { kind: 'symbol', text: 'execAdd', path: 'cmd/add.go', line: 12 },
      { kind: 'text', text: ' in ' },
      { kind: 'file', text: 'cmd/add.go', path: 'cmd/add.go' },
      { kind: 'text', text: ' saves via ' },
      { kind: 'symbol', text: 'Store.Save()', path: 'internal/utils/io.go', line: 30 },
      { kind: 'text', text: '.' },
    ]);
    expect(docTextParts('`internal/utils/io.go:7` and `internal/utils/` and `nope`', refs)).toEqual([
      { kind: 'file', text: 'internal/utils/io.go:7', path: 'internal/utils/io.go', line: 7 },
      { kind: 'text', text: ' and ' },
      { kind: 'component', text: 'internal/utils/', path: 'internal/utils' },
      { kind: 'text', text: ' and ' },
      { kind: 'code', text: 'nope' },
    ]);
  });

  it('leaves words, unknown paths and a lone backtick as text', () => {
    expect(docTextParts('The cmd package, e.g. other/x.go, uses `Save`', refs)).toEqual([
      { kind: 'text', text: 'The cmd package, e.g. other/x.go, uses ' },
      { kind: 'symbol', text: 'Save', path: 'internal/utils/io.go', line: 30 },
    ]);
    expect(docTextParts('a ` b', refs)).toEqual([{ kind: 'text', text: 'a ' }, { kind: 'text', text: '` b' }]);
    expect(docTextParts('`run`', refs)).toEqual([{ kind: 'symbol', text: 'run', path: 'cmd/add.go', line: 40 }]);
  });

  it('reads the architecture as lines, an older paragraph as one', () => {
    expect(architectureLines('- cmd: starts it\n\ninternal/utils: stores it')).toEqual(['cmd: starts it', 'internal/utils: stores it']);
    expect(architectureLines('One paragraph.')).toEqual(['One paragraph.']);
    expect(architectureLines(undefined)).toEqual([]);
  });
});
