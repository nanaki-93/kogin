// A read-only IDE view of the project: tree, editor tabs, highlighted source
// with issue markers, and an inspector. Changes always go through a task.

import { useEffect, useMemo, useRef, useState } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { ChevronRight, Copy, FileCode2, Folder, FolderOpen, Lightbulb, ListCollapse, PanelRight, Plus, RefreshCw, Search, Sparkles, X } from 'lucide-react';
import { useFile, useFindings, useIndex, useProject } from '@/api/hooks';
import type { FileInfo, Finding } from '@/api/types';
import { useActions, useBusy, type FileExplanation } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { openNewTask, openPalette } from '@/app/store';
import { setParam, useRoute } from '@/lib/router';
import { highlightLines } from '@/lib/highlight';
import { ancestors, buildTree, visibleRows } from '@/lib/tree';
import { rankItems } from '@/lib/fuzzy';
import { basename, companionTestPath, dirname, explainable, languageOf, plural } from '@/lib/util';
import { Badge, Button, EmptyState, Highlight, IconButton, Input, Kbd, Skeleton, cx } from '@/components/ui';
import { FileIcon, SeverityBadge, isUnresolvedFinding } from '@/components/domain';
import { useFeedback } from '@/components/overlays';
import { FileStages } from '@/features/analysis/FileStages';
import { FileAbout } from './FileAbout';

const tabsByCheckout = new Map<string, string[]>();

export function CodePage() {
  const route = useRoute();
  const { worktreePath } = useCheckout();
  const file = route.params.get('file') ?? '';
  const line = Number(route.params.get('line') ?? 0);
  const [tabs, setTabs] = useState<string[]>(() => tabsByCheckout.get(worktreePath) ?? []);
  const [inspector, setInspector] = useState(true);

  useEffect(() => {
    if (!file) return;
    setTabs(list => {
      const next = list.includes(file) ? list : [...list, file].slice(-10);
      tabsByCheckout.set(worktreePath, next);
      return next;
    });
  }, [file, worktreePath]);

  const close = (path: string) => {
    const index = tabs.indexOf(path);
    const next = tabs.filter(t => t !== path);
    tabsByCheckout.set(worktreePath, next);
    setTabs(next);
    if (path === file) setParam('file', next[Math.min(index, next.length - 1)]);
  };

  return (
    <div className="ide">
      <FileTree selected={file} />
      <section className="editor" aria-label="Editor">
        {tabs.length ? (
          <div className="editor-tabs" role="tablist" aria-label="Open files">
            {tabs.map(path => (
              <div key={path} role="tab" aria-selected={path === file} className={cx('editor-tab', path === file && 'is-active')} title={path}>
                <button type="button" className="editor-tab-label" onClick={() => setParam('file', path)}>
                  <FileIcon path={path} size={13} />
                  {basename(path)}
                </button>
                <button type="button" className="editor-tab-close" aria-label={`Close ${basename(path)}`} onClick={() => close(path)}>
                  <X size={12} aria-hidden />
                </button>
              </div>
            ))}
            <span className="spacer" />
            <IconButton icon={PanelRight} label={inspector ? 'Hide inspector' : 'Show inspector'} size="sm" active={inspector} onClick={() => setInspector(!inspector)} />
          </div>
        ) : null}
        {file ? (
          <Editor path={file} line={line} inspector={inspector} />
        ) : (
          <EmptyState icon={FileCode2} title="Open a file" action={<Button icon={Search} onClick={() => openPalette('files')} kbd="mod+P">Go to file</Button>}>
            Pick a file in the project tree. Source is read-only here; to change it, start a task.
          </EmptyState>
        )}
      </section>
    </div>
  );
}

function FileTree({ selected }: { selected: string }) {
  const index = useIndex();
  const files = index.data?.files ?? [];
  // An issue a task is fixing is still in these files until the task is merged.
  const findings = (useFindings().data?.findings ?? []).filter(isUnresolvedFinding);
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const [filter, setFilter] = useState('');
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(ancestors(selected)));
  const parent = useRef<HTMLDivElement>(null);
  const issueCount = useMemo(() => {
    const counts = new Map<string, number>();
    for (const f of findings) counts.set(f.location?.path, (counts.get(f.location?.path) ?? 0) + 1);
    return counts;
  }, [findings]);

  useEffect(() => {
    if (selected) setExpanded(set => new Set([...set, ...ancestors(selected)]));
  }, [selected]);

  const tree = useMemo(() => buildTree(files, f => f.path), [files]);
  const rows = useMemo(() => (filter.trim() ? null : visibleRows(tree, expanded)), [tree, expanded, filter]);
  const matches = useMemo(() => (filter.trim() ? rankItems(files, filter, f => f.path, 300) : []), [files, filter]);
  const count = rows ? rows.length : matches.length;
  const virtual = useVirtualizer({ count, getScrollElement: () => parent.current, estimateSize: () => 26, overscan: 12 });

  const toggle = (path: string) => setExpanded(set => {
    const next = new Set(set);
    if (next.has(path)) next.delete(path);
    else next.add(path);
    return next;
  });

  return (
    <aside className="tree-panel" aria-label="Project files">
      <div className="panel-head">
        <span className="panel-title">{project?.name ?? 'Project'}</span>
        <span className="spacer" />
        <IconButton icon={ListCollapse} label="Collapse folders" size="sm" onClick={() => setExpanded(new Set())} />
        {project ? <IconButton icon={RefreshCw} label="Reindex files" size="sm" onClick={() => void actions.reindex(project, worktreePath)} /> : null}
      </div>
      <div className="panel-search">
        <Input icon={Search} placeholder={`Filter ${plural(files.length, 'file')}…`} value={filter} onChange={e => setFilter(e.target.value)} aria-label="Filter files" />
      </div>
      <div className="tree-scroll" ref={parent}>
        {index.isLoading ? (
          <Skeleton lines={12} className="tree-skeleton" />
        ) : !files.length ? (
          <div className="panel-empty small muted">No indexed files. Reindex the project.</div>
        ) : (
          <div className="tree" role="tree" style={{ height: virtual.getTotalSize() }}>
            {virtual.getVirtualItems().map(item => {
              const style = { transform: `translateY(${item.start}px)` };
              if (!rows) {
                const match = matches[item.index];
                return (
                  <button key={match.item.path} type="button" role="treeitem" aria-selected={match.item.path === selected} className={cx('tree-row', match.item.path === selected && 'is-active')} style={style} onClick={() => setParam('file', match.item.path)}>
                    <FileIcon path={match.item.path} size={14} />
                    <span className="truncate">
                      <Highlight text={match.item.path} positions={match.positions} />
                    </span>
                  </button>
                );
              }
              const row = rows[item.index];
              const indent = { ...style, paddingLeft: 8 + row.depth * 14 };
              if (row.type === 'dir') {
                return (
                  <button key={row.path} type="button" role="treeitem" aria-expanded={row.open} className="tree-row" style={indent} onClick={() => toggle(row.path)}>
                    <ChevronRight size={13} className={cx('chev', row.open && 'is-open')} aria-hidden />
                    {row.open ? <FolderOpen size={14} className="folder-icon" aria-hidden /> : <Folder size={14} className="folder-icon" aria-hidden />}
                    <span className="truncate">{row.label}</span>
                  </button>
                );
              }
              const issues = issueCount.get(row.path) ?? 0;
              return (
                <button key={row.path} type="button" role="treeitem" aria-selected={row.path === selected} className={cx('tree-row', row.path === selected && 'is-active')} style={{ ...indent, paddingLeft: 8 + row.depth * 14 + 17 }} onClick={() => setParam('file', row.path)} title={row.path}>
                  <FileIcon path={row.path} size={14} />
                  <span className="truncate">{row.label}</span>
                  {issues ? <span className="tree-issue" title={plural(issues, 'open issue')}>{issues}</span> : null}
                </button>
              );
            })}
          </div>
        )}
      </div>
    </aside>
  );
}

function Editor({ path, line, inspector }: { path: string; line: number; inspector: boolean }) {
  const { data: info, isLoading, error } = useFile(path);
  const findings = (useFindings().data?.findings ?? []).filter(f => isUnresolvedFinding(f) && f.location?.path === path);
  const lines = useMemo(() => (info ? highlightLines(info.content ?? '', languageOf(path)) : []), [info, path]);
  const markers = useMemo(() => {
    const map = new Map<number, Finding[]>();
    for (const f of findings) {
      const n = f.location?.start_line;
      if (n) map.set(n, [...(map.get(n) ?? []), f]);
    }
    return map;
  }, [findings]);
  const scroller = useRef<HTMLDivElement>(null);
  const virtual = useVirtualizer({ count: lines.length, getScrollElement: () => scroller.current, estimateSize: () => 20, overscan: 30 });

  useEffect(() => {
    if (line > 0 && lines.length) virtual.scrollToIndex(line - 1, { align: 'center' });
  }, [line, lines.length]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="editor-split">
      <div className="editor-main">
        <div className="breadcrumbs" aria-label="File path">
          {path.split('/').map((part, i, all) => (
            <span key={i} className={cx('crumb', i === all.length - 1 && 'is-last')}>
              {part}
              {i < all.length - 1 ? <ChevronRight size={12} aria-hidden /> : null}
            </span>
          ))}
          <span className="spacer" />
          {info ? <span className="xs faint">{info.language} · {plural(lines.length, 'line')}</span> : null}
        </div>
        {isLoading ? (
          <div className="code-loading"><Skeleton lines={16} /></div>
        ) : error || !info ? (
          <EmptyState icon={FileCode2} title="File unavailable">{error instanceof Error ? error.message : 'It may have been removed or excluded from the index.'}</EmptyState>
        ) : info.binary ? (
          <EmptyState icon={FileCode2} title="Binary file">Binary files are not shown.</EmptyState>
        ) : (
          <div className="code-scroll" ref={scroller}>
            <div className="code" style={{ height: virtual.getTotalSize() }} aria-label={`Source of ${path}`}>
              {virtual.getVirtualItems().map(item => {
                const n = item.index + 1;
                const marks = markers.get(n);
                return (
                  <div key={item.index} className={cx('code-line', n === line && 'is-target', marks && 'has-issue')} style={{ transform: `translateY(${item.start}px)` }}>
                    <span className="code-ln" onClick={() => setParam('line', String(n))}>
                      {marks ? <span className="issue-mark" title={marks.map(m => m.title).join('\n')} /> : null}
                      {n}
                    </span>
                    <span className="code-text" dangerouslySetInnerHTML={{ __html: lines[item.index] || ' ' }} />
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
      {inspector && info ? <Inspector path={path} findings={findings} file={info} /> : null}
    </div>
  );
}

function Inspector({ path, findings, file }: { path: string; findings: Finding[]; file: FileInfo }) {
  const project = useProject().data;
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const busy = useBusy();
  const { toast } = useFeedback();
  const canExplain = explainable(file);
  const [result, setResult] = useState<FileExplanation | null>(null);
  useEffect(() => setResult(null), [path]);

  const newTask = () => {
    const companion = companionTestPath(path);
    openNewTask({ paths: companion ? [path, companion] : [path], title: '' });
  };

  return (
    <aside className="inspector" aria-label="File inspector">
      <div className="inspector-section">
        <div className="row">
          <div className="grow">
            <div className="inspector-title">{basename(path)}</div>
            <div className="xs faint truncate" title={path}>{dirname(path) || 'project root'}</div>
          </div>
          <IconButton icon={Copy} label="Copy path" size="sm" onClick={() => { void navigator.clipboard?.writeText(path); toast('Path copied.', 'success'); }} />
        </div>
        <div className="row-wrap">
          <Button size="sm" variant="primary" icon={Plus} onClick={newTask}>New task with this file</Button>
          <Button size="sm" icon={Sparkles} onClick={async () => { if (project) setResult((await actions.explainFile(project, worktreePath, { path, content_hash: file.content_hash })) ?? null); }} disabled={!project || !canExplain || Boolean(busy)} loading={busy === 'Explaining…'} title={canExplain ? undefined : 'Explain works on source files'}>Explain</Button>
        </div>
      </div>
      {result ? (
        <div className="inspector-section stack">
          <div className="row"><Lightbulb size={14} aria-hidden /><span className="strong small">Explanation</span><Badge tone="accent">AI</Badge></div>
          {result.summary ? <p className="small">{result.summary}</p> : null}
          {result.behavior?.length ? <ul className="small">{result.behavior.map((b, i) => <li key={i}>{b}</li>)}</ul> : null}
          {result.error_behavior?.length ? (<><div className="xs muted">Errors and edge cases</div><ul className="small">{result.error_behavior.map((b, i) => <li key={i}>{b}</li>)}</ul></>) : null}
        </div>
      ) : null}
      <FileAbout path={path} />
      <div className="inspector-section">
        <div className="row"><span className="strong small">Open issues</span><span className="count">{findings.length}</span></div>
        {findings.length ? (
          <ul className="mini-list">
            {findings.map(f => (
              <li key={f.id}>
                <button type="button" className="mini-row" onClick={() => setParam('line', String(f.location?.start_line ?? 1))}>
                  <SeverityBadge severity={f.severity} />
                  <span className="truncate grow">{f.title}</span>
                  {f.location?.start_line ? <span className="xs faint">L{f.location.start_line}</span> : null}
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="xs muted">No open issues in this file.</p>
        )}
      </div>
      <FileStages path={path} />
      <div className="inspector-section inspector-hint xs faint">
        <span><Kbd>mod+P</Kbd> go to file</span>
        <span>Click a line number to link to it</span>
      </div>
    </aside>
  );
}
