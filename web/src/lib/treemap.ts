// The project map: the project's folders and files as a tree sized by lines,
// with what each holds (open issues by severity, analysis results, the tasks
// that changed it, its main language), laid out as a squarified treemap.

export interface MapFile {
  path: string;
  lines: number;
  language?: string;
  /** Open issues by severity. */
  issues?: Record<string, number>;
  /** The index's analysis status: fresh, stale, failed, running or missing. */
  analysis?: string;
  /** Task ids that changed the file. */
  tasks?: string[];
}

export interface MapNode {
  name: string;
  /** Project-relative path; '' for the project. */
  path: string;
  lines: number;
  files: number;
  issues: Record<string, number>;
  /** Files per analysis status. */
  analysis: Record<string, number>;
  tasks: Set<string>;
  languages: Record<string, number>;
  children: MapNode[];
  /** Set on a file. */
  file?: MapFile;
}

export const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const;

function emptyNode(name: string, path: string): MapNode {
  return { name, path, lines: 0, files: 0, issues: {}, analysis: {}, tasks: new Set(), languages: {}, children: [] };
}

/**
 * The project tree. Every file counts at least one line so empty files stay
 * visible; a folder holding a single folder is merged with it
 * ("cmd/daemon"), so the map shows real branches.
 */
export function buildMapTree(files: MapFile[], rootName: string): MapNode {
  const root = emptyNode(rootName, '');
  const folders = new Map<string, MapNode>([['', root]]);
  for (const file of files) {
    const parts = file.path.split('/');
    let parent = root;
    for (let depth = 0; depth < parts.length - 1; depth++) {
      const path = parts.slice(0, depth + 1).join('/');
      let folder = folders.get(path);
      if (!folder) {
        folder = emptyNode(parts[depth], path);
        folders.set(path, folder);
        parent.children.push(folder);
      }
      parent = folder;
    }
    const leaf = emptyNode(parts[parts.length - 1], file.path);
    leaf.file = file;
    parent.children.push(leaf);
  }
  total(root);
  return merge(root);
}

function total(node: MapNode): void {
  if (node.file) {
    const file = node.file;
    node.lines = Math.max(1, file.lines);
    node.files = 1;
    for (const [severity, count] of Object.entries(file.issues ?? {})) node.issues[severity] = count;
    node.analysis[file.analysis || 'missing'] = 1;
    for (const id of file.tasks ?? []) node.tasks.add(id);
    if (file.language) node.languages[file.language] = node.lines;
    return;
  }
  for (const child of node.children) {
    total(child);
    node.lines += child.lines;
    node.files += child.files;
    for (const [severity, count] of Object.entries(child.issues)) node.issues[severity] = (node.issues[severity] ?? 0) + count;
    for (const [status, count] of Object.entries(child.analysis)) node.analysis[status] = (node.analysis[status] ?? 0) + count;
    for (const id of child.tasks) node.tasks.add(id);
    for (const [language, lines] of Object.entries(child.languages)) node.languages[language] = (node.languages[language] ?? 0) + lines;
  }
  node.children.sort((a, b) => b.lines - a.lines || a.name.localeCompare(b.name));
}

function merge(node: MapNode): MapNode {
  node.children = node.children.map(merge);
  if (node.path && !node.file && node.children.length === 1 && !node.children[0].file) {
    const only = node.children[0];
    return { ...only, name: `${node.name}/${only.name}` };
  }
  return node;
}

/** The node at `path`, or the root when there is none. */
export function findNode(root: MapNode, path: string): MapNode {
  if (!path) return root;
  let node = root;
  for (;;) {
    const next = node.children.find(child => !child.file && (path === child.path || path.startsWith(child.path + '/')));
    if (!next) return node;
    if (next.path === path) return next;
    node = next;
  }
}

/** The folders from the project down to `node`, for the breadcrumb. */
export function trail(root: MapNode, path: string): MapNode[] {
  const out = [root];
  let node = root;
  while (node.path !== path) {
    const next = node.children.find(child => !child.file && (path === child.path || path.startsWith(child.path + '/')));
    if (!next) break;
    out.push(next);
    node = next;
  }
  return out;
}

export function openIssues(node: Pick<MapNode, 'issues'>): number {
  return Object.values(node.issues).reduce((sum, count) => sum + count, 0);
}

/** The most severe open issue's severity, or ''. */
export function worstSeverity(node: Pick<MapNode, 'issues'>): string {
  return SEVERITIES.find(severity => (node.issues[severity] ?? 0) > 0) ?? '';
}

/** The language with the most lines. */
export function mainLanguage(node: Pick<MapNode, 'languages'>): string {
  return Object.entries(node.languages).sort((a, b) => b[1] - a[1])[0]?.[0] ?? '';
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * Squarified treemap (Bruls, Huizing, van Wijk): lays `values` (largest
 * first) into `rect` in rows whose cells stay as close to squares as they can.
 * Returns one rectangle per value, in the order given.
 */
export function squarify(values: number[], rect: Rect): Rect[] {
  const out: Rect[] = new Array(values.length);
  const sum = values.reduce((a, b) => a + Math.max(0, b), 0);
  if (!values.length || sum <= 0 || rect.w <= 0 || rect.h <= 0) return values.map(() => ({ x: rect.x, y: rect.y, w: 0, h: 0 }));
  const scale = (rect.w * rect.h) / sum;
  const areas = values.map(value => Math.max(0, value) * scale);
  let free = { ...rect };
  let row: number[] = [];
  const worst = (indexes: number[], side: number) => {
    const list = indexes.map(i => areas[i]);
    const total = list.reduce((a, b) => a + b, 0);
    const max = Math.max(...list);
    const min = Math.min(...list);
    return Math.max((side * side * max) / (total * total), (total * total) / (side * side * min));
  };
  const place = (indexes: number[]) => {
    const total = indexes.reduce((a, i) => a + areas[i], 0);
    if (free.w >= free.h) {
      const width = free.h > 0 ? total / free.h : 0;
      let y = free.y;
      for (const i of indexes) {
        const height = width > 0 ? areas[i] / width : 0;
        out[i] = { x: free.x, y, w: width, h: height };
        y += height;
      }
      free = { x: free.x + width, y: free.y, w: Math.max(0, free.w - width), h: free.h };
    } else {
      const height = free.w > 0 ? total / free.w : 0;
      let x = free.x;
      for (const i of indexes) {
        const width = height > 0 ? areas[i] / height : 0;
        out[i] = { x, y: free.y, w: width, h: height };
        x += width;
      }
      free = { x: free.x, y: free.y + height, w: free.w, h: Math.max(0, free.h - height) };
    }
  };
  for (let i = 0; i < areas.length; i++) {
    if (areas[i] <= 0) {
      out[i] = { x: free.x, y: free.y, w: 0, h: 0 };
      continue;
    }
    const side = Math.min(free.w, free.h);
    if (!row.length || worst([...row, i], side) <= worst(row, side)) row.push(i);
    else {
      place(row);
      row = [i];
    }
  }
  if (row.length) place(row);
  return out;
}
