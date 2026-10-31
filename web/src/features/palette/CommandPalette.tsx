// ⌘K: jump to any file, task, issue or page, or run a command. ⌘P opens it
// in file mode.

import { useMemo, useState } from 'react';
import { Command } from 'cmdk';
import * as Dialog from '@radix-ui/react-dialog';
import { Activity, BookOpen, CircleAlert, Code2, GitMerge, Inbox, Keyboard, ListTodo, MessageCircleQuestion, Moon, Palette as PaletteIcon, Play, Plus, RefreshCw, Settings, ShieldCheck, Sun } from 'lucide-react';
import { useFindings, useIndex, useProject, useTasks, useTrust } from '@/api/hooks';
import { useActions } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { DARK_PALETTES, openNewTask, setUI, useUI } from '@/app/store';
import { navigate } from '@/lib/router';
import { rankItems } from '@/lib/fuzzy';
import { basename, dirname } from '@/lib/util';
import { taskStatus } from '@/lib/readiness';
import { FileIcon, isInProgressFinding, isUnresolvedFinding, StatusGlyph } from '@/components/domain';
import { Highlight, Kbd } from '@/components/ui';

export function CommandPalette() {
  const open = useUI(s => s.paletteOpen);
  const mode = useUI(s => s.paletteMode);
  return (
    <Dialog.Root open={open} onOpenChange={next => setUI({ paletteOpen: next })}>
      <Dialog.Portal>
        <Dialog.Overlay className="overlay overlay-light" />
        <Dialog.Content className="palette" aria-label="Command palette">
          <Dialog.Title className="sr-only">Command palette</Dialog.Title>
          <Dialog.Description className="sr-only">Search files, tasks, issues and commands.</Dialog.Description>
          {open ? <Palette mode={mode} /> : null}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Palette({ mode }: { mode: 'all' | 'files' | 'commands' }) {
  const [search, setSearch] = useState('');
  const files = useIndex().data?.files ?? [];
  const { tasks } = useTasks();
  const findings = (useFindings().data?.findings ?? []).filter(isUnresolvedFinding);
  const project = useProject().data;
  const trust = useTrust().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const theme = useUI(s => s.theme);
  const palette = useUI(s => s.darkPalette);
  const close = () => setUI({ paletteOpen: false });
  const run = (fn: () => void) => () => {
    close();
    fn();
  };

  const fileHits = useMemo(() => rankItems(files, search, f => f.path, mode === 'files' ? 50 : 8), [files, search, mode]);
  const taskHits = useMemo(() => (mode === 'files' ? [] : rankItems(tasks, search, t => t.title || t.id, 6)), [tasks, search, mode]);
  const issueHits = useMemo(() => (mode === 'files' || !search.trim() ? [] : rankItems(findings, search, f => f.title, 5)), [findings, search, mode]);

  const commands = [
    { id: 'new', label: 'New task', icon: Plus, keys: 'C', run: () => openNewTask() },
    { id: 'inbox', label: 'Go to Inbox', icon: Inbox, keys: 'G H', run: () => navigate('/') },
    { id: 'tasks', label: 'Go to Tasks', icon: ListTodo, keys: 'G T', run: () => navigate('/tasks') },
    { id: 'code', label: 'Go to Code', icon: Code2, keys: 'G C', run: () => navigate('/code') },
    { id: 'issues', label: 'Go to Issues', icon: CircleAlert, keys: 'G I', run: () => navigate('/issues') },
    { id: 'analysis', label: 'Go to Analysis', icon: Activity, keys: 'G A', run: () => navigate('/analysis') },
    { id: 'docs', label: 'Go to Docs', icon: BookOpen, keys: 'G D', run: () => navigate('/docs') },
    { id: 'git', label: 'Go to Branches', icon: GitMerge, keys: 'G B', run: () => navigate('/git') },
    { id: 'settings', label: 'Open Settings', icon: Settings, keys: 'G S', run: () => navigate('/settings') },
    { id: 'ask', label: 'Ask the codebase', icon: MessageCircleQuestion, keys: '', run: () => navigate('/docs/ask') },
    ...(project
      ? [
          { id: 'analyze', label: 'Analyze project', icon: Play, keys: '', run: () => void actions.runAnalysis(project, worktreePath) },
          { id: 'reindex', label: 'Reindex project files', icon: RefreshCw, keys: '', run: () => void actions.reindex(project, worktreePath) },
          { id: 'trust', label: trust?.trusted ? 'Revoke check trust' : 'Allow checks for this project', icon: ShieldCheck, keys: '', run: () => void actions.setTrust(project, worktreePath, trust, !trust?.trusted) },
        ]
      : []),
    { id: 'theme', label: theme === 'light' ? 'Switch to dark theme' : 'Switch to light theme', icon: theme === 'light' ? Moon : Sun, keys: '', run: () => setUI({ theme: theme === 'light' ? 'dark' : 'light' }) },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: Keyboard, keys: '?', run: () => setUI({ shortcutsOpen: true }) },
    ...DARK_PALETTES.filter(option => option.id !== palette || theme === 'light').map(option => ({ id: `palette-${option.id}`, label: `Dark theme: ${option.label}`, icon: PaletteIcon, keys: '', run: () => setUI({ theme: 'dark', darkPalette: option.id }) })),
  ];
  const commandHits = mode === 'files' ? [] : rankItems(commands, search, c => c.label, 20);

  return (
    <Command label="Command palette" shouldFilter={false} loop>
      <Command.Input value={search} onValueChange={setSearch} placeholder={mode === 'files' ? 'Go to file…' : 'Search files, tasks, issues, commands…'} className="palette-input" autoFocus />
      <Command.List className="palette-list">
        <Command.Empty className="palette-empty">No results.</Command.Empty>
        {commandHits.length && (search.trim() || mode === 'commands') ? (
          <Command.Group heading="Commands">
            {commandHits.map(({ item, positions }) => (
              <Command.Item key={item.id} value={`cmd-${item.id}`} onSelect={run(item.run)} className="palette-item">
                <item.icon size={15} aria-hidden />
                <span className="grow"><Highlight text={item.label} positions={positions} /></span>
                {item.keys ? <span className="row">{item.keys.split(' ').map(k => <Kbd key={k}>{k}</Kbd>)}</span> : null}
              </Command.Item>
            ))}
          </Command.Group>
        ) : null}
        {taskHits.length ? (
          <Command.Group heading="Tasks">
            {taskHits.map(({ item, positions }) => (
              <Command.Item key={item.id} value={`task-${item.id}`} onSelect={run(() => navigate(`/tasks/${item.id}`, { wt: item.worktreePath || undefined }))} className="palette-item">
                <StatusGlyph status={taskStatus(item).status} />
                <span className="grow truncate"><Highlight text={item.title || item.id} positions={positions} /></span>
                <span className="xs muted">{taskStatus(item).label}</span>
              </Command.Item>
            ))}
          </Command.Group>
        ) : null}
        {fileHits.length && (search.trim() || mode === 'files') ? (
          <Command.Group heading="Files">
            {fileHits.map(({ item, positions }) => {
              const base = item.path.lastIndexOf('/') + 1;
              return (
                <Command.Item key={item.path} value={`file-${item.path}`} onSelect={run(() => navigate('/code', { file: item.path }))} className="palette-item">
                  <FileIcon path={item.path} />
                  <span className="truncate">
                    <Highlight text={basename(item.path)} positions={positions.filter(p => p >= base).map(p => p - base)} />
                    <span className="faint xs"> {dirname(item.path)}</span>
                  </span>
                </Command.Item>
              );
            })}
          </Command.Group>
        ) : null}
        {issueHits.length ? (
          <Command.Group heading="Issues">
            {issueHits.map(({ item, positions }) => (
              <Command.Item key={item.id} value={`issue-${item.id}`} onSelect={run(() => navigate('/issues', { id: item.id, status: isInProgressFinding(item) ? 'in_progress' : undefined }))} className="palette-item">
                <CircleAlert size={15} aria-hidden />
                <span className="grow truncate"><Highlight text={item.title} positions={positions} /></span>
                <span className="xs muted">{item.severity}</span>
              </Command.Item>
            ))}
          </Command.Group>
        ) : null}
        {mode === 'all' && search.trim().split(/\s+/).length >= 3 ? (
          <Command.Group heading="Ask">
            <Command.Item value="ask-question" onSelect={run(() => navigate('/docs/ask', { q: search.trim() }))} className="palette-item">
              <MessageCircleQuestion size={15} aria-hidden />
              <span className="grow truncate">Ask the codebase: “{search.trim()}”</span>
            </Command.Item>
          </Command.Group>
        ) : null}
        {!search.trim() && mode === 'all' ? (
          <Command.Group heading="Commands">
            {commands.slice(0, 8).map(item => (
              <Command.Item key={item.id} value={`cmd-${item.id}`} onSelect={run(item.run)} className="palette-item">
                <item.icon size={15} aria-hidden />
                <span className="grow">{item.label}</span>
                {item.keys ? <span className="row">{item.keys.split(' ').map(k => <Kbd key={k}>{k}</Kbd>)}</span> : null}
              </Command.Item>
            ))}
          </Command.Group>
        ) : null}
      </Command.List>
      <div className="palette-foot xs faint">
        <span><Kbd>↑</Kbd><Kbd>↓</Kbd> navigate</span>
        <span><Kbd>↵</Kbd> open</span>
        <span><Kbd>Esc</Kbd> close</span>
      </div>
    </Command>
  );
}
