import { describe, expect, it } from 'vitest';
import { componentOf, docsSection, firstSentence, walkthroughsThrough } from './docs';

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
    expect(firstSentence(undefined)).toBe('');
  });

  it('sends older links to the page that replaced theirs', () => {
    expect(docsSection('runbook')).toBe('start');
    expect(docsSection('scenarios')).toBe('flows');
    expect(docsSection('glossary')).toBe('overview');
    expect(docsSection(undefined)).toBe('overview');
    expect(docsSection('components')).toBe('components');
  });
});
