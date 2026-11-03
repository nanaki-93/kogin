// The application frame: sidebar navigation, top bar with project and
// checkout, the routed page, and a status bar for live state.

import { useEffect, useMemo, type ReactNode } from 'react';
import * as Menu from '@radix-ui/react-dropdown-menu';
import {
  Activity, AlertTriangle, Bot, BookOpen, ChevronDown, CircleAlert, Code2, FolderOpen, GitBranch, GitMerge, Inbox, Keyboard, ListTodo, PanelLeft, Plus, Search, Settings, ShieldCheck, ShieldOff, Sparkles,
} from 'lucide-react';
import { useRoute, navigate } from '@/lib/router';
import { useFindings, useGitWorkspace, useMainProject, useModels, useOverview, useProject, useSession, useStatus, useTasks, useTrust } from '@/api/hooks';
import type { Connection } from '@/api/events';
import { useBusy } from '@/app/actions';
import { openWorkspace, useCheckout } from '@/app/checkout';
import { openNewTask, openPalette, setUI, useUI } from '@/app/store';
import { taskStatus } from '@/lib/readiness';
import { runActive, runProgress } from '@/lib/analysis';
import { basename, plural } from '@/lib/util';
import type { Project } from '@/api/types';
import { Dot, IconButton, Kbd, Spinner, Tip, cx } from '@/components/ui';
import { BrandMark } from '@/components/BrandMark';
import { formatKeys } from '@/app/shortcuts';
import { isOpenFinding } from '@/components/domain';

interface NavItem {
  path: string;
  label: string;
  icon: typeof Inbox;
  count?: number;
  tone?: 'accent' | 'info' | 'warning';
  keys: string;
}

export function Shell({ children, connection }: { children: ReactNode; connection: Connection }) {
  const route = useRoute();
  const collapsed = useUI(s => s.sidebarCollapsed);
  const theme = useUI(s => s.theme);
  const palette = useUI(s => s.darkPalette);
  const { tasks } = useTasks();
  const findings = useFindings().data?.findings ?? [];
  const git = useGitWorkspace().data;

  useEffect(() => {
    const root = document.documentElement;
    const media = typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-color-scheme: dark)') : null;
    const apply = () => {
      const dark = theme === 'dark' || (theme === 'system' && Boolean(media?.matches));
      root.dataset.theme = dark ? 'dark' : 'light';
      root.dataset.palette = palette;
    };
    apply();
    media?.addEventListener('change', apply);
    return () => media?.removeEventListener('change', apply);
  }, [theme, palette]);

  const needsYou = tasks.filter(task => ['needs_you', 'ready'].includes(taskStatus(task).status)).length;
  const running = tasks.filter(task => taskStatus(task).status === 'running').length;
  const openIssues = findings.filter(isOpenFinding).length;
  const toMerge = (git?.worktrees ?? []).filter(tree => !tree.current && tree.mergeable).length;
  const analyzing = runActive(useOverview().data?.analysis_run);

  const items: NavItem[] = useMemo(
    () => [
      { path: '/', label: 'Inbox', icon: Inbox, count: needsYou || undefined, tone: 'accent', keys: 'g h' },
      { path: '/tasks', label: 'Tasks', icon: ListTodo, count: running || undefined, tone: 'info', keys: 'g t' },
      { path: '/code', label: 'Code', icon: Code2, keys: 'g c' },
      { path: '/issues', label: 'Issues', icon: CircleAlert, count: openIssues || undefined, keys: 'g i' },
      { path: '/analysis', label: 'Analysis', icon: Activity, count: analyzing ? 1 : undefined, tone: 'info', keys: 'g a' },
      { path: '/docs', label: 'Docs', icon: BookOpen, keys: 'g d' },
      { path: '/git', label: 'Branches', icon: GitMerge, count: toMerge || undefined, tone: 'warning', keys: 'g b' },
    ],
    [needsYou, running, openIssues, toMerge, analyzing],
  );

  const active = (path: string) => (path === '/' ? route.path === '/' : route.path === path || route.path.startsWith(path + '/'));

  return (
    <div className={cx('shell', collapsed && 'is-collapsed')}>
      <aside className="sidebar" aria-label="Primary">
        <div className="sidebar-brand">
          <BrandMark size={18} />
          <span className="sidebar-brand-name sidebar-label">KASURI</span>
        </div>
        <ProjectSwitcher collapsed={collapsed} />
        <button type="button" className="sidebar-new" onClick={() => openNewTask()} aria-label="New task">
          <Plus size={15} aria-hidden />
          <span className="sidebar-label">New task</span>
          <Kbd>C</Kbd>
        </button>
        <div className="nav-section" aria-hidden>Workspace</div>
        <nav className="nav" aria-label="Workspace">
          {items.map(item => (
            <Tip key={item.path} label={<span className="row">{item.label} <span className="row">{formatKeys(item.keys).map(k => <Kbd key={k}>{k}</Kbd>)}</span></span>} side="right">
              <a href={`#${item.path}`} className={cx('nav-item', active(item.path) && 'is-active')} aria-current={active(item.path) ? 'page' : undefined} onClick={event => { event.preventDefault(); openWorkspace(item.path); }}>
                <item.icon size={16} aria-hidden />
                <span className="sidebar-label">{item.label}</span>
                {item.count ? <span className={cx('nav-count', item.tone && `nav-count-${item.tone}`)}>{item.count}</span> : null}
              </a>
            </Tip>
          ))}
        </nav>
        <div className="spacer" />
        <nav className="nav" aria-label="Preferences">
          <a href="#/settings" className={cx('nav-item', active('/settings') && 'is-active')} onClick={event => { event.preventDefault(); openWorkspace('/settings'); }}>
            <Settings size={16} aria-hidden />
            <span className="sidebar-label">Settings</span>
          </a>
          <button type="button" className="nav-item" onClick={() => setUI({ sidebarCollapsed: !collapsed })} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
            <PanelLeft size={16} aria-hidden />
            <span className="sidebar-label">Collapse</span>
          </button>
        </nav>
      </aside>
      <div className="main">
        <TopBar />
        <main className="page" id="main">{children}</main>
        <StatusBar connection={connection} running={running} />
      </div>
    </div>
  );
}

function ProjectSwitcher({ collapsed }: { collapsed: boolean }) {
  const project = useMainProject().data;
  const name = project?.name || (project?.path ? basename(project.path) : 'Kasuri');
  return (
    <button type="button" className="project-switcher" onClick={() => navigate('/settings/project')} title={project?.path}>
      <span className="project-mark" aria-hidden>
        {name.slice(0, 1).toUpperCase()}
      </span>
      {!collapsed ? (
        <span className="grow">
          <span className="project-name truncate">{name}</span>
          <span className="project-sub truncate">{projectSubtitle(project)}</span>
        </span>
      ) : null}
    </button>
  );
}

function CheckoutMenu() {
  const git = useGitWorkspace().data;
  const { worktreePath, setCheckout } = useCheckout();
  const trees = git?.worktrees ?? [];
  const current = worktreePath ? trees.find(t => t.path === worktreePath) : trees.find(t => t.current);
  const label = current?.branch || (worktreePath ? basename(worktreePath) : git?.branch) || 'main checkout';
  if (!git?.available) return null;
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <button type="button" className="checkout-btn" aria-label="Switch checkout">
          <GitBranch size={14} aria-hidden />
          <span className="truncate">{label}</span>
          {worktreePath ? <span className="badge badge-info">worktree</span> : null}
          <ChevronDown size={13} aria-hidden />
        </button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content className="menu" align="start" sideOffset={6}>
          <Menu.Label className="menu-label">Checkout</Menu.Label>
          {trees.map(tree => {
            const value = tree.current ? '' : tree.path;
            return (
              <Menu.Item key={tree.path} className={cx('menu-item', value === worktreePath && 'is-selected')} onSelect={() => setCheckout(value)}>
                <GitBranch size={14} aria-hidden />
                <span className="grow truncate">{tree.branch || basename(tree.path)}</span>
                <span className="faint xs">{tree.current ? 'main' : (tree.sessions ?? []).length ? `${tree.sessions!.length} task` : 'worktree'}</span>
              </Menu.Item>
            );
          })}
          <Menu.Separator className="menu-sep" />
          <Menu.Item className="menu-item" onSelect={() => navigate('/git')}>
            <GitMerge size={14} aria-hidden />
            Manage branches and worktrees
          </Menu.Item>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}

function TopBar() {
  const route = useRoute();
  const section = route.segments[0] ?? '';
  const title = pageTitle(route.path);
  const taskId = section === 'tasks' && route.segments[1] && route.segments[1] !== 'new' ? route.segments[1] : '';
  return (
    <header className="topbar">
      <nav className="crumbs grow" aria-label="Breadcrumb">
        <CheckoutMenu />
        <span className="topbar-sep" aria-hidden>/</span>
        {taskId ? (
          <>
            <button type="button" className="crumb-link" onClick={() => openWorkspace('/tasks')}>
              {title}
            </button>
            <span className="topbar-sep" aria-hidden>/</span>
            <TaskCrumb id={taskId} />
          </>
        ) : (
          <span className="topbar-title truncate" aria-current="page">{title}</span>
        )}
      </nav>
      <button type="button" className="search-trigger" onClick={() => openPalette()} aria-label="Search and run commands">
        <Search size={14} aria-hidden />
        <span className="grow">Search files, tasks, commands…</span>
        <span className="row">{formatKeys('mod+k').map(k => <Kbd key={k}>{k}</Kbd>)}</span>
      </button>
      <div className="row grow topbar-end" />
    </header>
  );
}

function TaskCrumb({ id }: { id: string }) {
  const { worktreePath } = useCheckout();
  const title = useSession(id, worktreePath, false).data?.title;
  return <span className="topbar-title truncate" aria-current="page">{title || id}</span>;
}

function projectSubtitle(project: Project | undefined): string {
  if (!project?.project_id) return 'No project open';
  const count = Number(project.file_count);
  return [project.type, Number.isFinite(count) && count > 0 ? plural(count, 'file') : ''].filter(Boolean).join(' · ') || project.path;
}

function pageTitle(path: string): string {
  if (path === '/') return 'Inbox';
  const first = path.split('/')[1];
  return { tasks: 'Tasks', code: 'Code', issues: 'Issues', analysis: 'Analysis', setup: 'Setup', docs: 'Docs', git: 'Branches', settings: 'Settings' }[first] ?? '';
}

function StatusBar({ connection, running }: { connection: Connection; running: number }) {
  const busy = useBusy();
  const status = useStatus().data;
  const trust = useTrust().data;
  const project = useProject().data;
  const models = useModels().data;
  const analysis = useOverview().data?.analysis_run;
  const remote = Object.values(models?.scopes ?? {}).filter(m => m.remote_provider).length;
  return (
    <footer className="statusbar" aria-label="Status">
      <span className="status-item">
        <Dot tone={connection === 'live' ? 'success' : connection === 'connecting' ? 'warning' : 'danger'} pulse={connection === 'connecting'} />
        {connection === 'live' ? 'Live' : connection === 'connecting' ? 'Connecting…' : 'Daemon offline'}
      </span>
      {busy ? (
        <span className="status-item">
          <Spinner size={12} />
          {busy}
        </span>
      ) : null}
      {running ? (
        <button type="button" className="status-item status-link" onClick={() => navigate('/tasks', { filter: 'running' })}>
          <Bot size={13} aria-hidden />
          {running} agent run{running === 1 ? '' : 's'}
        </button>
      ) : null}
      {analysis && runActive(analysis) ? (
        <button type="button" className="status-item status-link" onClick={() => navigate('/analysis')}>
          <Activity size={13} className="tone-info" aria-hidden />
          Analyzing {runProgress(analysis).percent}%
        </button>
      ) : null}
      <span className="spacer" />
      {project ? (
        <button type="button" className="status-item status-link" onClick={() => navigate('/settings/trust')}>
          {trust?.trusted ? <ShieldCheck size={13} className="tone-success" aria-hidden /> : <ShieldOff size={13} className="tone-warning" aria-hidden />}
          {trust?.trusted ? 'Checks allowed' : 'Checks need trust'}
        </button>
      ) : null}
      <button type="button" className="status-item status-link" onClick={() => navigate('/settings/models')} title={models?.config_file ? `Configuration: ${models.config_file}` : undefined}>
        {models?.config_file === '' ? <AlertTriangle size={13} className="tone-warning" aria-hidden /> : <Sparkles size={13} aria-hidden />}
        {models?.config_file === '' ? 'No config.yaml · default models' : remote ? `${remote} remote model${remote === 1 ? '' : 's'}` : 'Local models'}
      </button>
      {project ? (
        <span className="status-item" title={project.path}>
          <FolderOpen size={13} aria-hidden />
          <span className="truncate">{project.path}</span>
        </span>
      ) : null}
      <span className="status-item faint">v{status?.version ?? '—'}</span>
      <IconButton icon={Keyboard} size="sm" label="Keyboard shortcuts (?)" onClick={() => setUI({ shortcutsOpen: true })} className="status-help" />
    </footer>
  );
}
