// Routing and global wiring: data cache, live events, overlays, shortcuts.

import { useMemo } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';
import { FeedbackProvider, Modal } from '@/components/overlays';
import { Kbd } from '@/components/ui';
import { useDaemonEvents } from '@/api/events';
import { useProjectRules, useSetup } from '@/api/hooks';
import { setTestPatterns } from '@/lib/rules';
import { Shell } from '@/layout/Shell';
import { navigate, useRoute } from '@/lib/router';
import { openWorkspace } from '@/app/checkout';
import { openNewTask, openPalette, setUI, useUI } from '@/app/store';
import { formatKeys, useShortcuts, type Shortcut } from '@/app/shortcuts';
import { InboxPage } from '@/features/inbox/InboxPage';
import { TaskListPage } from '@/features/tasks/TaskListPage';
import { TaskPage } from '@/features/tasks/TaskPage';
import { NewTaskDialog } from '@/features/tasks/NewTaskDialog';
import { CodePage } from '@/features/code/CodePage';
import { IssuesPage } from '@/features/issues/IssuesPage';
import { HealthPage } from '@/features/health/HealthPage';
import { AnalysisPage } from '@/features/analysis/AnalysisPage';
import { SetupPage } from '@/features/setup/SetupPage';
import { DocsPage } from '@/features/docs/DocsPage';
import { GitPage } from '@/features/git/GitPage';
import { SettingsPage } from '@/features/settings/SettingsPage';
import { CommandPalette } from '@/features/palette/CommandPalette';
import { useEffect } from 'react';

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { staleTime: 5_000, refetchOnWindowFocus: true, retry: 1 },
      mutations: { retry: false },
    },
  });
}

export function App({ client }: { client?: QueryClient }) {
  const queryClient = useMemo(() => client ?? createQueryClient(), [client]);
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipPrimitive.Provider delayDuration={350} skipDelayDuration={200}>
        <FeedbackProvider>
          <Workspace />
        </FeedbackProvider>
      </TooltipPrimitive.Provider>
    </QueryClientProvider>
  );
}

function Workspace() {
  const connection = useDaemonEvents();
  // Test files are classified as the daemon classifies them; the patterns are
  // adopted during render so every page below uses them in this pass.
  const testPatterns = useProjectRules().data?.test_patterns;
  useMemo(() => setTestPatterns(testPatterns), [testPatterns]);
  const route = useRoute();
  const shortcuts = useMemo<Shortcut[]>(
    () => [
      { keys: 'mod+k', label: 'Command palette', group: 'General', run: () => openPalette() },
      { keys: 'mod+p', label: 'Go to file', group: 'General', run: () => openPalette('files') },
      { keys: 'c', label: 'New task', group: 'General', run: () => openNewTask() },
      { keys: '?', label: 'Keyboard shortcuts', group: 'General', run: () => setUI({ shortcutsOpen: true }) },
      { keys: '/', label: 'Search', group: 'General', run: () => openPalette() },
      { keys: 'g h', label: 'Inbox', group: 'Go to', run: () => openWorkspace('/') },
      { keys: 'g t', label: 'Tasks', group: 'Go to', run: () => openWorkspace('/tasks') },
      { keys: 'g c', label: 'Code', group: 'Go to', run: () => openWorkspace('/code') },
      { keys: 'g i', label: 'Issues', group: 'Go to', run: () => openWorkspace('/issues') },
      { keys: 'g a', label: 'Analysis', group: 'Go to', run: () => openWorkspace('/analysis') },
      { keys: 'g d', label: 'Docs', group: 'Go to', run: () => openWorkspace('/docs') },
      { keys: 'g l', label: 'Health', group: 'Go to', run: () => openWorkspace('/health') },
      { keys: 'g b', label: 'Branches', group: 'Go to', run: () => openWorkspace('/git') },
      { keys: 'g s', label: 'Settings', group: 'Go to', run: () => openWorkspace('/settings') },
    ],
    [],
  );
  useShortcuts(shortcuts);
  useSetupOffer();

  useEffect(() => {
    if (route.segments[0] === 'tasks' && route.segments[1] === 'new') {
      openNewTask();
      navigate('/tasks', {}, { replace: true });
    }
  }, [route.segments]);

  return (
    <>
      <Shell connection={connection}>
        <Page />
      </Shell>
      <CommandPalette />
      <NewTaskDialog />
      <ShortcutsDialog shortcuts={shortcuts} />
    </>
  );
}

/** Projects whose setup was already offered in this window. */
const offeredSetup = new Set<string>();

/** Opens setup once per project that has no configuration of its own yet. */
function useSetupOffer() {
  const setup = useSetup().data;
  const route = useRoute();
  useEffect(() => {
    if (setup?.state !== 'needed' || route.segments[0] === 'setup' || offeredSetup.has(setup.config_file)) return;
    offeredSetup.add(setup.config_file);
    navigate('/setup');
  }, [setup, route.segments]);
}

function Page() {
  const route = useRoute();
  const [first, second, third] = route.segments;
  switch (first) {
    case undefined:
      return <InboxPage />;
    case 'tasks':
      return second && second !== 'new' ? <TaskPage key={second} id={second} tab={third ?? 'overview'} /> : <TaskListPage />;
    case 'code':
      return <CodePage />;
    case 'issues':
      return <IssuesPage />;
    case 'analysis':
      return <AnalysisPage />;
    case 'health':
      return <HealthPage />;
    case 'setup':
      return <SetupPage />;
    case 'docs':
      return <DocsPage section={second ?? 'overview'} />;
    case 'git':
      return <GitPage />;
    case 'settings':
      return <SettingsPage section={second ?? 'project'} />;
    default:
      return <InboxPage />;
  }
}

const EXTRA: { group: string; keys: string; label: string }[] = [
  { group: 'Lists', keys: 'j', label: 'Next item' },
  { group: 'Lists', keys: 'k', label: 'Previous item' },
  { group: 'Lists', keys: 'Enter', label: 'Open' },
  { group: 'Lists', keys: 'Delete', label: 'Delete task' },
  { group: 'Review', keys: 'ArrowRight', label: 'Next hunk (j in whole file: next file)' },
  { group: 'Review', keys: 'ArrowLeft', label: 'Previous hunk (k in whole file: previous file)' },
  { group: 'Review', keys: 'a', label: 'Hunk looks right' },
  { group: 'Review', keys: 'm', label: 'Comment on the hunk' },
  { group: 'Review', keys: 'v', label: 'Mark file viewed' },
  { group: 'Review', keys: 'f', label: 'Hunks or whole file' },
  { group: 'Review', keys: 's', label: 'Toggle split diff' },
  { group: 'Inbox', keys: 'Enter', label: 'Start what is next up' },
  { group: 'Inbox', keys: 's', label: 'Skip it' },
  { group: 'Project map', keys: 'Backspace', label: 'Up one folder' },
  { group: 'Composer', keys: 'mod+Enter', label: 'Start agents' },
  { group: 'Issues', keys: 'j', label: 'Next issue' },
  { group: 'Issues', keys: 'k', label: 'Previous issue' },
  { group: 'Issues', keys: 'f', label: 'Fix with agents' },
  { group: 'Issues', keys: 'x', label: 'Dismiss or reopen' },
  { group: 'Issues', keys: 'Escape', label: 'Close the detail' },
];

function ShortcutsDialog({ shortcuts }: { shortcuts: Shortcut[] }) {
  const open = useUI(s => s.shortcutsOpen);
  const all = [...shortcuts.map(s => ({ group: s.group, keys: s.keys, label: s.label })), ...EXTRA];
  const groups = [...new Set(all.map(s => s.group))];
  return (
    <Modal open={open} onOpenChange={next => setUI({ shortcutsOpen: next })} title="Keyboard shortcuts" size="md">
      <div className="shortcut-grid">
        {groups.map(group => (
          <section key={group}>
            <h4 className="shortcut-group">{group}</h4>
            {all.filter(s => s.group === group).map(s => (
              <div key={group + s.keys} className="shortcut-row">
                <span className="grow">{s.label}</span>
                <span className="row">{formatKeys(s.keys).map((k, i) => <Kbd key={i}>{k}</Kbd>)}</span>
              </div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  );
}
