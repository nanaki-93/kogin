// The project map: every folder and file as a block sized by its lines,
// nested while there is room (a folder's block holds its contents), colored by
// what you want to see: open issues, analysis freshness, the tasks that
// changed it, or its language. Click a folder to zoom in, a file to open it
// in Code; the trail and Backspace go back up.

import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { ArrowUp, Map as MapIcon } from 'lucide-react';
import { useFindings, useIndex, useProject, useTasks } from '@/api/hooks';
import { usePageKeys } from '@/app/shortcuts';
import { navigate, setParam, useRoute } from '@/lib/router';
import { buildMapTree, findNode, mainLanguage, openIssues, squarify, trail, worstSeverity, type MapFile, type MapNode, type Rect } from '@/lib/treemap';
import { formatTokens } from '@/lib/usage';
import { plural } from '@/lib/util';
import { EmptyState, IconButton, Segmented, Skeleton, cx } from '@/components/ui';
import { isCountedOpenFinding } from '@/components/domain';

type ColorBy = 'issues' | 'analysis' | 'tasks' | 'language';
const HEADER = 22;
const MIN_GROUP = { w: 130, h: 84 };
const MAX_DEPTH = 3;

export function ProjectMap() {
  const route = useRoute();
  const index = useIndex();
  const project = useProject().data;
  const findings = useFindings().data?.findings;
  const tasks = useTasks().data;
  const focus = route.params.get('path') ?? '';
  const colorBy = (route.params.get('color') as ColorBy) || 'issues';
  const [hover, setHover] = useState<{ node: MapNode; x: number; y: number } | null>(null);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const frame = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const element = frame.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setBox({ w: Math.floor(entry.contentRect.width), h: Math.floor(entry.contentRect.height) }));
    observer.observe(element);
    return () => observer.disconnect();
  }, [index.isLoading]);

  const root = useMemo(() => {
    const issues = new Map<string, Record<string, number>>();
    for (const finding of findings ?? []) {
      if (!isCountedOpenFinding(finding) || !finding.location?.path) continue;
      const counts = issues.get(finding.location.path) ?? {};
      counts[finding.severity] = (counts[finding.severity] ?? 0) + 1;
      issues.set(finding.location.path, counts);
    }
    const touched = new Map<string, string[]>();
    for (const task of tasks ?? []) for (const path of task.paths ?? []) touched.set(path, [...(touched.get(path) ?? []), task.id]);
    const files: MapFile[] = (index.data?.files ?? []).filter(file => !file.binary).map(file => ({
      path: file.path, lines: file.line_count ?? 0, language: file.language, issues: issues.get(file.path), analysis: file.analysis_status, tasks: touched.get(file.path),
    }));
    return buildMapTree(files, project?.name || 'Project');
  }, [index.data, findings, tasks, project?.name]);

  const node = findNode(root, focus);
  const path = trail(root, node.path);
  const up = () => setParam('path', path.length > 1 ? path[path.length - 2].path || undefined : undefined);
  const open = (target: MapNode) => (target.file ? navigate('/code', { file: target.path }) : setParam('path', target.path || undefined));
  usePageKeys(key => {
    if ((key === 'Backspace' || key === 'Escape') && node.path) up();
    else return false;
    return true;
  });

  if (index.isLoading) return <Skeleton lines={12} />;
  if (!root.files) return <EmptyState icon={MapIcon} title="No files to map">Index the project to see its map.</EmptyState>;

  const hovered = (target: MapNode) => (event: { clientX: number; clientY: number }) => setHover({ node: target, x: event.clientX, y: event.clientY });
  // A folder with room shows its own contents inside it, down to MAX_DEPTH
  // levels; one without room is a single block.
  const place = (child: MapNode, r: Rect, depth: number): ReactNode => {
    const roomy = !child.file && child.children.length > 0 && depth < MAX_DEPTH && r.w >= MIN_GROUP.w && r.h >= MIN_GROUP.h;
    if (!roomy) {
      return (
        <button key={child.path} type="button" className={cx('map-tile', child.file ? 'is-file' : 'is-folder')} style={{ left: r.x, top: r.y, width: Math.max(0, r.w), height: Math.max(0, r.h), ...tone(child, colorBy) }}
          onClick={event => { event.stopPropagation(); open(child); }} onMouseMove={hovered(child)} onMouseLeave={() => setHover(null)} aria-label={label(child)}>
          {r.w >= 56 && r.h >= 24 ? (
            <span className="map-label">
              <span className="map-name truncate">{child.name}</span>
              {r.h >= 42 ? <span className="map-meta truncate">{meta(child, colorBy)}</span> : null}
            </span>
          ) : null}
        </button>
      );
    }
    const inner = squarify(child.children.map(c => c.lines), { x: 2, y: HEADER, w: r.w - 4, h: r.h - HEADER - 2 });
    return (
      <div key={child.path} className="map-group" style={{ left: r.x, top: r.y, width: r.w, height: r.h, ...tone(child, colorBy, true) }}>
        <button type="button" className="map-group-head" onClick={() => open(child)} onMouseMove={hovered(child)} onMouseLeave={() => setHover(null)} aria-label={label(child)}>
          <span className="map-name truncate">{child.name}</span>
          <span className="map-meta">{meta(child, colorBy)}</span>
        </button>
        {child.children.map((grand, j) => (inner[j] ? place(grand, inner[j], depth + 1) : null))}
      </div>
    );
  };
  const rects = squarify(node.children.map(child => child.lines), { x: 0, y: 0, w: box.w, h: box.h });

  return (
    <div className="project-map stack">
      <div className="page-head">
        <div className="grow">
          <h1 className="page-title">Project map</h1>
          <p className="page-sub">Each block is a folder or a file, sized by its lines. Click a folder to zoom in, a file to open it.</p>
        </div>
        <Segmented label="Color by" value={colorBy} onChange={value => setParam('color', value === 'issues' ? undefined : value)} options={[{ value: 'issues', label: 'Open issues' }, { value: 'analysis', label: 'Analysis' }, { value: 'tasks', label: 'Tasks' }, { value: 'language', label: 'Language' }]} />
      </div>
      <nav className="map-trail" aria-label="Map trail">
        <IconButton icon={ArrowUp} label="Up one folder (Backspace)" size="sm" disabled={!node.path} onClick={up} />
        {path.map((step, i) => (
          <span key={step.path || 'root'} className="row">
            {i ? <span className="faint">/</span> : null}
            <button type="button" className={cx('link-btn small', i === path.length - 1 && 'strong')} onClick={() => setParam('path', step.path || undefined)}>{step.name}</button>
          </span>
        ))}
        <span className="spacer" />
        <span className="xs muted">{plural(node.files, 'file')} · {formatTokens(node.lines)} lines · {plural(openIssues(node), 'open issue')}</span>
      </nav>
      <div className="map-frame" ref={frame}>
        {node.children.map((child, i) => (rects[i] ? place(child, rects[i], 0) : null))}
      </div>
      <Legend colorBy={colorBy} root={root} />
      {hover ? <Tip node={hover.node} x={hover.x} y={hover.y} /> : null}
    </div>
  );
}

const SEVERITY_TONE: Record<string, string> = { critical: 'var(--danger)', high: 'color-mix(in srgb, var(--danger) 60%, var(--surface-2))', medium: 'color-mix(in srgb, var(--warning) 50%, var(--surface-2))', low: 'color-mix(in srgb, var(--info) 35%, var(--surface-2))' };
const ANALYSIS_TONE: Record<string, string> = { fresh: 'color-mix(in srgb, var(--success) 30%, var(--surface-2))', stale: 'color-mix(in srgb, var(--warning) 40%, var(--surface-2))', failed: 'color-mix(in srgb, var(--danger) 50%, var(--surface-2))', running: 'color-mix(in srgb, var(--info) 40%, var(--surface-2))', missing: 'var(--surface-2)' };
const LANGUAGE_HUES = ['var(--info)', 'var(--success)', 'var(--nav-accent)', 'var(--warning)', '#e879a6', '#5fd4d0', '#c49a6c', '#9aa3ff'];

/** The block's background for the chosen color. */
function tone(node: MapNode, colorBy: ColorBy, group = false): CSSProperties {
  let color = 'var(--surface-2)';
  if (colorBy === 'issues') color = SEVERITY_TONE[worstSeverity(node)] ?? color;
  else if (colorBy === 'analysis') color = ANALYSIS_TONE[mainStatus(node)] ?? color;
  else if (colorBy === 'tasks') color = node.tasks.size ? `color-mix(in srgb, var(--nav-accent) ${Math.min(70, 20 + node.tasks.size * 12)}%, var(--surface-2))` : color;
  else color = `color-mix(in srgb, ${LANGUAGE_HUES[hash(mainLanguage(node)) % LANGUAGE_HUES.length]} 38%, var(--surface-2))`;
  return group ? { background: `color-mix(in srgb, ${color} 30%, var(--bg-sunken))` } : { background: color };
}

/** The analysis status of most of the node's files; failed once a fifth of them failed. */
function mainStatus(node: MapNode): string {
  if ((node.analysis.failed ?? 0) * 5 >= node.files) return 'failed';
  const entries = Object.entries(node.analysis).sort((a, b) => b[1] - a[1]);
  return entries[0]?.[0] ?? 'missing';
}

function meta(node: MapNode, colorBy: ColorBy): string {
  const size = `${formatTokens(node.lines)} lines`;
  if (colorBy === 'issues') return openIssues(node) ? `${size} · ${plural(openIssues(node), 'issue')}` : size;
  if (colorBy === 'analysis') return node.file ? `${size} · ${mainStatus(node)}` : `${size} · ${node.analysis.fresh ?? 0}/${node.files} fresh${node.analysis.failed ? ` · ${node.analysis.failed} failed` : ''}`;
  if (colorBy === 'tasks') return node.tasks.size ? `${size} · ${plural(node.tasks.size, 'task')}` : size;
  return `${size} · ${mainLanguage(node) || 'other'}`;
}

function label(node: MapNode): string {
  return `${node.path || node.name}: ${node.file ? '' : `${plural(node.files, 'file')}, `}${node.lines} lines, ${plural(openIssues(node), 'open issue')}`;
}

function hash(text: string): number {
  let value = 0;
  for (const char of text) value = (value * 31 + char.charCodeAt(0)) >>> 0;
  return value;
}

function Tip({ node, x, y }: { node: MapNode; x: number; y: number }) {
  const issues = openIssues(node);
  const style = { left: Math.min(x + 14, window.innerWidth - 300), top: Math.min(y + 14, window.innerHeight - 160) };
  return (
    <div className="map-tip" style={style} role="tooltip">
      <div className="mono small strong">{node.path || node.name}</div>
      <div className="xs muted">{node.file ? '' : `${plural(node.files, 'file')} · `}{node.lines.toLocaleString()} lines{mainLanguage(node) ? ` · ${mainLanguage(node)}` : ''}</div>
      <div className="xs">{issues ? `${plural(issues, 'open issue')}: ${Object.entries(node.issues).map(([severity, count]) => `${count} ${severity}`).join(', ')}` : 'No open issues'}</div>
      <div className="xs muted">Analysis: {Object.entries(node.analysis).map(([status, count]) => (node.file ? status : `${count} ${status}`)).join(', ') || 'missing'}</div>
      {node.tasks.size ? <div className="xs muted">Changed by {plural(node.tasks.size, 'task')}</div> : null}
    </div>
  );
}

function Legend({ colorBy, root }: { colorBy: ColorBy; root: MapNode }) {
  let items: [string, string][];
  if (colorBy === 'issues') items = [['No open issues', 'var(--surface-2)'], ...(['low', 'medium', 'high', 'critical'] as const).map(s => [`Worst is ${s}`, SEVERITY_TONE[s]] as [string, string])];
  else if (colorBy === 'analysis') items = Object.entries(ANALYSIS_TONE).map(([status, color]) => [status[0].toUpperCase() + status.slice(1), color]);
  else if (colorBy === 'tasks') items = [['No task', 'var(--surface-2)'], ['1 task', tone({ ...root, tasks: new Set(['a']) }, 'tasks').background as string], ['4 or more', tone({ ...root, tasks: new Set(['a', 'b', 'c', 'd']) }, 'tasks').background as string]];
  else items = Object.entries(root.languages).sort((a, b) => b[1] - a[1]).slice(0, 8).map(([language]) => [language, tone({ ...root, languages: { [language]: 1 } }, 'language').background as string]);
  return (
    <div className="map-legend" aria-label="Legend">
      {items.map(([text, color]) => (
        <span key={text}><i style={{ background: color }} />{text}</span>
      ))}
      <span className="spacer" />
      <span className="faint">Backspace goes up a folder</span>
    </div>
  );
}
