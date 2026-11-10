import { describe, expect, it } from 'vitest';
import { architectureLines, architectureParts, architectureRoles, componentOf, componentTrail, declarationExcerpt, docTextParts, docsSection, firstSentence, guideStops, symbolRefs, walkthroughsThrough } from './docs';

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

describe('the architecture and the guides, by component', () => {
  const architecture = 'main.go: entrypoint that calls `cmd.Execute()`.\n`cmd`: Cobra subcommands.\ninternal/utils: encryption and atomic save.\nREADME: not a part.\nmain.go: said twice.';
  const add = { title: 'Add a password', summary: '', steps: [
    { text: 'Unlock the vault.', path: 'cmd/add.go', symbol: 'execAddCmd', line: 146 },
    { text: 'Verify the master password.', path: 'internal/utils/utils.go', symbol: 'GetPwdObjAfterCheckMaster', line: 684 },
    { text: 'Read the name.', path: 'cmd/add.go', symbol: 'readPwdName', line: 35 },
    { text: 'Ask before overwriting.', path: 'cmd/add.go' },
    { text: 'Save.', path: 'internal/utils/utils.go', symbol: 'SaveFileErr', line: 655 },
  ] };
  const subcommand = { title: 'Add a subcommand', summary: '', steps: [
    { text: 'Unlock the vault first.', path: 'internal/utils/utils.go', symbol: 'GetPwdObjAfterCheckMaster', line: 684 },
  ] };

  it('reads what each component does from the architecture lines', () => {
    const roles = architectureRoles(architecture, components);
    expect(roles.get('.')).toBe('entrypoint that calls `cmd.Execute()`.');
    expect(roles.get('cmd')).toBe('Cobra subcommands.');
    expect(roles.get('internal/utils')).toBe('encryption and atomic save.');
    expect(roles.has('internal')).toBe(false);
    expect(architectureRoles('One paragraph about the whole project: it has parts.', components).size).toBe(0);
    // A line that names no component, or names one a second time, stays a line of its own.
    expect(architectureParts(architecture, components).rest).toEqual(['README: not a part.', 'main.go: said twice.']);
    expect(architectureParts('One paragraph about the whole project: it has parts.', components).rest).toHaveLength(1);
  });

  it('names the components a guide passes through, each once, in the order it first reaches them', () => {
    expect(componentTrail(add, components)).toEqual(['cmd', 'internal/utils']);
    expect(componentTrail({ title: '', summary: '', steps: [] }, components)).toEqual([]);
  });

  it('lists the declarations the guides stop at in a component, most visited first', () => {
    const stops = guideStops('internal/utils', [add], [subcommand], components);
    expect(stops.map(stop => stop.symbol)).toEqual(['GetPwdObjAfterCheckMaster', 'SaveFileErr']);
    expect(stops[0]).toMatchObject({ path: 'internal/utils/utils.go', line: 684, text: 'Verify the master password.', flows: [1], changes: [1] });
    expect(guideStops('cmd', [add], [subcommand], components).map(stop => stop.symbol)).toEqual(['readPwdName', 'execAddCmd']);
  });
});

describe('a declaration excerpt', () => {
  const go = ['package cmd', '', 'func add(name string) error {', '\tif name == "" {', '\t\treturn errEmpty', '\t}', '', '\treturn save(name)', '}', '', 'var limit = 64', 'type Store struct {', '\tPath string', '}'].join('\n');

  it('runs from the declaration to the line that closes it', () => {
    expect(declarationExcerpt(go, 3)).toEqual({ first: 3, lines: go.split('\n').slice(2, 9), more: 0 });
    expect(declarationExcerpt(go, 12)?.lines).toEqual(['type Store struct {', '\tPath string', '}']);
  });

  it('reads on past a signature that closes on its own line', () => {
    const wrapped = ['func save(', '\tname string,', ') error {', '\treturn nil', '}', 'var x = 1'].join('\n');
    expect(declarationExcerpt(wrapped, 1)?.lines).toHaveLength(5);
  });

  it('drops the indentation a method has inside its class', () => {
    const java = ['class Vault {', '    void save() {', '        write();', '    }', '}'].join('\n');
    expect(declarationExcerpt(java, 2)?.lines).toEqual(['void save() {', '    write();', '}']);
  });

  it('is one line for a declaration with no body', () => {
    expect(declarationExcerpt(go, 11)).toEqual({ first: 11, lines: ['var limit = 64'], more: 0 });
  });

  it('follows indentation where no brace closes the body', () => {
    const py = ['def add(name):', '    if not name:', '        raise ValueError', '', '    return save(name)', '', 'def other():', '    pass'].join('\n');
    expect(declarationExcerpt(py, 1)?.lines).toEqual(['def add(name):', '    if not name:', '        raise ValueError', '', '    return save(name)']);
  });

  it('stops at the most lines asked for and says how many it left out', () => {
    expect(declarationExcerpt(go, 3, 3)).toEqual({ first: 3, lines: go.split('\n').slice(2, 5), more: 4 });
  });

  it('is nothing for a line the file does not have or a blank one', () => {
    expect(declarationExcerpt(go, 99)).toBeNull();
    expect(declarationExcerpt(go, 2)).toBeNull();
    expect(declarationExcerpt(go, 0)).toBeNull();
  });
});
