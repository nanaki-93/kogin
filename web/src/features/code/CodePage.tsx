// A read-only IDE and Code X-Ray workbench: project tree, editor tabs,
// highlighted source with enclosing AST scope, interactive line-range actions,
// and an X-Ray / Overview inspector. Changes always go through a task.

import { useEffect, useMemo, useRef, useState } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import {
  Braces,
  ChevronRight,
  Copy,
  FileCode2,
  Folder,
  FolderOpen,
  GraduationCap,
  ListCollapse,
  MessageCircleQuestion,
  PanelRight,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  X,
} from 'lucide-react';
import { useFile, useFindings, useIndex, useProject, useProjectRules } from '@/api/hooks';
import type { FileInfo, Finding } from '@/api/types';
import { useActions } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { openNewTask, openPalette } from '@/app/store';
import { handQuestion } from '@/features/docs/AskPanel';
import { FileStages } from '@/features/analysis/FileStages';
import { SidebarPanel } from '@/layout/SidebarPanel';
import { highlightLines } from '@/lib/highlight';
import { rankItems } from '@/lib/fuzzy';
import { navigate, setParam, useRoute } from '@/lib/router';
import { enclosingSymbol, mergeOutlineWithComplexity, symbolKindShort, type EnrichedSymbol } from '@/lib/symbols';
import { ancestors, buildTree, visibleRows } from '@/lib/tree';
import { basename, companionTestPath, dirname, languageOf, plural } from '@/lib/util';
import { Badge, Button, EmptyState, Highlight, IconButton, Input, Kbd, Segmented, Skeleton, cx } from '@/components/ui';
import { FileIcon, SeverityBadge, isUnresolvedFinding } from '@/components/domain';
import { useFeedback } from '@/components/overlays';
import { FileAbout } from './FileAbout';

const tabsByCheckout = new Map<string, string[]>();

type InspectorMode = 'xray' | 'overview';

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

  const tools = (
    <>
      <IconButton icon={ListCollapse} label="Collapse folders" size="sm" onClick={() => setExpanded(new Set())} />
      {project ? <IconButton icon={RefreshCw} label="Reindex files" size="sm" onClick={() => void actions.reindex(project, worktreePath)} /> : null}
    </>
  );
  return (
    <SidebarPanel title="Code" actions={tools}>
      <aside className="tree-panel" aria-label="Project files">
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
    </SidebarPanel>
  );
}

function Editor({ path, line, inspector }: { path: string; line: number; inspector: boolean }) {
  const { data: info, isLoading, error } = useFile(path);
  const rules = useProjectRules().data;
  const complexityBudget = rules?.budgets?.function_complexity ?? 15;
  const findings = (useFindings().data?.findings ?? []).filter(f => isUnresolvedFinding(f) && f.location?.path === path);
  const lines = useMemo(() => (info ? highlightLines(info.content ?? '', languageOf(path)) : []), [info, path]);
  const symbols = useMemo(
    () => mergeOutlineWithComplexity(info?.outline, info?.functions, complexityBudget),
    [complexityBudget, info?.functions, info?.outline],
  );

  const [selection, setSelection] = useState<{ anchor: number; end: number } | null>(null);
  useEffect(() => {
    setSelection(null);
  }, [path]);

  const selStart = selection ? Math.min(selection.anchor, selection.end) : 0;
  const selEnd = selection ? Math.max(selection.anchor, selection.end) : 0;
  const focusLine = selStart || line;
  const scope = useMemo(() => enclosingSymbol(symbols, focusLine), [focusLine, symbols]);

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

  const onLineClick = (n: number, shiftKey: boolean) => {
    if (shiftKey && (selection || line > 0)) {
      const anchor = selection ? selection.anchor : line;
      setSelection({ anchor, end: n });
      return;
    }
    setSelection({ anchor: n, end: n });
    setParam('line', String(n));
  };

  const askSelection = (mode: 'explain' | 'critique') => {
    const label = selStart === selEnd ? `line ${selStart}` : `lines ${selStart}–${selEnd}`;
    const inFn = scope ? ` (inside ${scope.name})` : '';
    const q = mode === 'explain'
      ? `Explain ${path} ${label}${inFn}: what invariant it maintains, how data flows through it, and how callers rely on it.`
      : `Give a senior systems engineering critique of ${path} ${label}${inFn}: check invariants, concurrency/cancellation, time & space complexity, failure modes, and subtle edge cases.`;
    handQuestion(q);
    navigate('/docs/ask', { q });
  };

  const scopeTaskFromSelection = () => {
    const companion = companionTestPath(path);
    const label = selStart === selEnd ? `L${selStart}` : `L${selStart}–${selEnd}`;
    openNewTask({
      paths: companion ? [path, companion] : [path],
      title: '',
      description: `Focus on ${path}:${label}${scope ? ` (${scope.name})` : ''}.`,
    });
  };

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
          {info ? (
            <span className="xs faint">
              {info.language} · {plural(lines.length, 'line')}
              {symbols.length ? ` · ${plural(symbols.length, 'symbol')}` : ''}
            </span>
          ) : null}
        </div>

        {scope || selection ? (
          <div className="code-workbench-bar">
            {scope ? (
              <div className="code-scope-pill" title={`Enclosing declaration (${scope.start_line}–${scope.end_line})`}>
                <span className="sym-kind mono">{symbolKindShort(scope.kind)}</span>
                <button type="button" className="link-btn mono xs strong" onClick={() => setParam('line', String(scope.start_line))}>
                  {scope.name}
                </button>
                <span className="xs faint mono">L{scope.start_line}–{scope.end_line}</span>
                {scope.complexity !== undefined ? (
                  <ComplexityBadge complexity={scope.complexity} state={scope.complexityState} budget={complexityBudget} />
                ) : null}
              </div>
            ) : null}
            <span className="spacer" />
            {selection ? (
              <div className="code-selection-bar" role="region" aria-label="Selected lines">
                <span className="mono xs strong tone-accent">
                  {selStart === selEnd ? `L${selStart}` : `L${selStart}–${selEnd}`}
                </span>
                <Button size="sm" variant="ghost" icon={MessageCircleQuestion} onClick={() => askSelection('explain')}>
                  Explain
                </Button>
                <Button size="sm" variant="ghost" icon={GraduationCap} onClick={() => askSelection('critique')}>
                  Senior critique
                </Button>
                <Button size="sm" variant="secondary" icon={Sparkles} onClick={scopeTaskFromSelection}>
                  Scope task
                </Button>
                <IconButton icon={X} label="Clear line selection" size="sm" onClick={() => setSelection(null)} />
              </div>
            ) : null}
          </div>
        ) : null}

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
                const inSelection = selStart > 0 && n >= selStart && n <= selEnd;
                return (
                  <div
                    key={item.index}
                    className={cx('code-line', (n === line || inSelection) && 'is-target', inSelection && 'is-selected', marks && 'has-issue')}
                    style={{ transform: `translateY(${item.start}px)` }}
                  >
                    <span className="code-ln" onClick={e => onLineClick(n, e.shiftKey)} title="Click to select line; Shift+Click to select a range">
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
      {inspector && info ? (
        <Inspector
          path={path}
          findings={findings}
          file={info}
          symbols={symbols}
          activeLine={focusLine}
          complexityBudget={complexityBudget}
        />
      ) : null}
    </div>
  );
}

function ComplexityBadge({ complexity, state, budget }: { complexity: number; state?: 'ok' | 'near' | 'over'; budget: number }) {
  const label = state === 'over' ? `C${complexity}!` : `C${complexity}`;
  const title = state === 'over'
    ? `Cyclomatic complexity ${complexity} exceeds budget ${budget}`
    : `Cyclomatic complexity ${complexity} (budget ${budget})`;
  return (
    <span className={cx('complexity-pill', 'mono', state && `is-${state}`)} title={title}>
      {label}
    </span>
  );
}

function Inspector({
  path,
  findings,
  file,
  symbols,
  activeLine,
  complexityBudget,
}: {
  path: string;
  findings: Finding[];
  file: FileInfo;
  symbols: EnrichedSymbol[];
  activeLine: number;
  complexityBudget: number;
}) {
  const { toast } = useFeedback();
  const [mode, setMode] = useState<InspectorMode>('xray');
  const [symQuery, setSymQuery] = useState('');
  const [showAllImports, setShowAllImports] = useState(false);

  const newTask = () => {
    const companion = companionTestPath(path);
    openNewTask({ paths: companion ? [path, companion] : [path], title: '' });
  };

  const filteredSymbols = useMemo(() => {
    const q = symQuery.trim().toLowerCase();
    if (!q) return symbols;
    return symbols.filter(sym => sym.name.toLowerCase().includes(q) || sym.kind.toLowerCase().includes(q));
  }, [symQuery, symbols]);

  const imports = file.imports ?? [];
  const visibleImports = showAllImports ? imports : imports.slice(0, 8);
  const overBudgetCount = symbols.filter(s => s.complexityState === 'over').length;

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
          {file.binary ? null : (
            <Button size="sm" icon={MessageCircleQuestion} onClick={() => navigate('/docs/ask', { q: `What does ${path} do, and where is it used?` })} title="Ask the code about this file: an agent reads it and answers with checked lines">Ask about it</Button>
          )}
        </div>
        {!file.binary ? (
          <Segmented
            label="Inspector view"
            value={mode}
            onChange={setMode}
            options={[
              { value: 'xray', label: 'X-Ray', icon: Braces, count: symbols.length || undefined },
              { value: 'overview', label: 'Overview', count: findings.length || undefined },
            ]}
          />
        ) : null}
      </div>

      {mode === 'xray' && !file.binary ? (
        <>
          <section className="inspector-section stack" aria-label="Symbol outline">
            <div className="row">
              <span className="strong small grow">X-Ray symbols</span>
              {overBudgetCount > 0 ? (
                <Badge tone="warning" title={`Max function complexity budget: ${complexityBudget}`}>
                  {overBudgetCount} over C{complexityBudget}
                </Badge>
              ) : symbols.length ? (
                <span className="xs faint mono">max C{complexityBudget}</span>
              ) : null}
            </div>
            {symbols.length > 5 ? (
              <Input
                icon={Search}
                value={symQuery}
                onChange={e => setSymQuery(e.target.value)}
                placeholder="Filter symbols…"
                aria-label="Filter symbols"
              />
            ) : null}
            {filteredSymbols.length ? (
              <ul className="xray-outline" aria-label="Declarations in file">
                {filteredSymbols.map(sym => {
                  const active = activeLine >= sym.start_line && activeLine <= sym.end_line;
                  return (
                    <li key={`${sym.kind}:${sym.name}:${sym.start_line}`}>
                      <button
                        type="button"
                        className={cx('xray-symbol-row', active && 'is-active')}
                        onClick={() => setParam('line', String(sym.start_line))}
                        title={`${sym.kind} ${sym.name} (lines ${sym.start_line}–${sym.end_line})`}
                      >
                        <span className="sym-kind mono">{symbolKindShort(sym.kind)}</span>
                        <span className="mono xs truncate grow">{sym.name}</span>
                        {sym.complexity !== undefined ? (
                          <ComplexityBadge complexity={sym.complexity} state={sym.complexityState} budget={complexityBudget} />
                        ) : null}
                        <span className="xs faint mono">L{sym.start_line}</span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            ) : (
              <p className="xs muted">{symbols.length ? 'No symbols match filter.' : 'No top-level declarations parsed in this file.'}</p>
            )}
          </section>

          {imports.length ? (
            <section className="inspector-section stack" aria-label="File imports">
              <div className="row">
                <span className="strong small grow">Imports</span>
                <span className="count">{imports.length}</span>
              </div>
              <div className="chips">
                {visibleImports.map(imp => (
                  <span key={imp} className="chip mono xs" title={imp}>{imp}</span>
                ))}
              </div>
              {imports.length > 8 ? (
                <button type="button" className="link-btn xs" onClick={() => setShowAllImports(v => !v)}>
                  {showAllImports ? 'Show fewer' : `Show all ${imports.length} imports`}
                </button>
              ) : null}
            </section>
          ) : null}
        </>
      ) : null}

      <FileAbout key={path} path={path} />
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
        <span>Click or Shift+Click line numbers to inspect a range</span>
      </div>
    </aside>
  );
}
