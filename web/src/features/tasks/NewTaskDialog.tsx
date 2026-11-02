// The task composer: describe the outcome, pick the files agents may change,
// state how you'll judge it, and start. Modeled on Linear's issue composer.

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { ArrowUp, FilePlus, FileSearch, FlaskConical, GitBranch, Play, Plus, Save, X } from 'lucide-react';
import { useGitWorkspace, useIndex, useProject, useTrust } from '@/api/hooks';
import { useActions, useBusy, type DraftTask } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { setUI, useUI } from '@/app/store';
import { rankItems } from '@/lib/fuzzy';
import { companionTestPath, dirname, basename, isTestPath, newFilePath, plural, unique } from '@/lib/util';
import { Button, Highlight, Kbd, Segmented, Textarea, Tip, cx } from '@/components/ui';
import { FileIcon, KINDS } from '@/components/domain';
import { composerStartBlocker, ownWorktreeBlocker } from '@/lib/workflow';
import { issueSteps } from '@/lib/issues';

export const MAX_TASK_PATHS = 8;

// A new task gets its own worktree by default, so several run in parallel.
const EMPTY: DraftTask = { title: '', kind: 'fix', description: '', paths: [], criteria: [], start: 'agents', autoRepair: true, worktree: true };

export function NewTaskDialog() {
  const prefill = useUI(s => s.newTask);
  const open = prefill !== null;
  return (
    <Dialog.Root open={open} onOpenChange={next => !next && setUI({ newTask: null })}>
      <Dialog.Portal>
        <Dialog.Overlay className="overlay" />
        <Dialog.Content className="modal modal-lg composer" aria-label="New task">
          {open ? <Composer prefill={prefill} /> : null}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Composer({ prefill }: { prefill: Partial<DraftTask> }) {
  const [draft, setDraft] = useState<DraftTask>({ ...EMPTY, ...prefill });
  const [error, setError] = useState('');
  const project = useProject().data;
  const trust = useTrust().data;
  const files = useIndex().data?.files ?? [];
  const { worktreePath } = useCheckout();
  const git = useGitWorkspace().data;
  const actions = useActions();
  const busy = useBusy();
  const titleRef = useRef<HTMLTextAreaElement>(null);
  // Only the main checkout makes worktrees; a task opened in one stays there.
  const worktreeBlocker = worktreePath ? '' : ownWorktreeBlocker(git, Boolean(trust?.trusted));
  const ownWorktree = !worktreePath && !worktreeBlocker && draft.worktree;
  // Why Start agents is unavailable, shown beside it rather than a silent disabled button.
  const startBlocker = composerStartBlocker(draft.paths, ownWorktree, Boolean(trust?.trusted), isTestPath);
  const steps = issueSteps(draft.issues, draft.findingTargets?.filter(target => draft.paths.includes(target.path)));

  useEffect(() => titleRef.current?.focus(), []);

  const update = (patch: Partial<DraftTask>) => {
    setDraft(d => ({ ...d, ...patch }));
    setError('');
  };

  function addPath(path: string) {
    if (draft.paths.includes(path)) return;
    const next = [...draft.paths, path];
    const companion = companionTestPath(path);
    if (companion && !next.includes(companion)) next.push(companion);
    if (unique(next).length > MAX_TASK_PATHS) {
      setError(`A task can change at most ${MAX_TASK_PATHS} files. Split larger work into several tasks.`);
      return;
    }
    update({ paths: unique(next) });
  }

  async function submit(start: DraftTask['start']) {
    if (!project) return;
    if (!draft.title.trim()) return setError('Give the task a title.');
    if (!draft.paths.length) return setError('Choose at least one file the agents may change.');
    if (start === 'agents' && startBlocker) return setError(startBlocker);
    const id = await actions.createTask(project, worktreePath, { ...draft, worktree: ownWorktree, start });
    if (id) setUI({ newTask: null });
  }

  const known = new Set(files.map(f => f.path));
  return (
    <form
      className="composer-form"
      onSubmit={event => {
        event.preventDefault();
        void submit(startBlocker ? 'draft' : 'agents');
      }}
      onKeyDown={(event: KeyboardEvent) => {
        if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
          event.preventDefault();
          void submit(startBlocker ? 'draft' : 'agents');
        }
      }}
    >
      <div className="composer-head">
        <span className="composer-crumb">
          {project?.name ?? 'Project'} <span className="faint">›</span> New task
        </span>
        <Dialog.Close asChild>
          <button type="button" className="icon-btn" aria-label="Close">
            <X size={16} aria-hidden />
          </button>
        </Dialog.Close>
      </div>
      <Dialog.Title className="sr-only">New task</Dialog.Title>
      <Dialog.Description className="sr-only">Describe a fix or feature, choose the files agents may change and how the result will be judged.</Dialog.Description>
      <div className="composer-body">
        <Textarea ref={titleRef} className="composer-title" rows={1} placeholder="What should change?" value={draft.title} onChange={e => update({ title: e.target.value.replace(/\n/g, ' ') })} aria-label="Task title" maxLength={200} />
        <Textarea className="composer-desc" rows={4} placeholder="Add context: the bug you see, the behavior you want, constraints the agents must respect…" value={draft.description} onChange={e => update({ description: e.target.value })} aria-label="Description" />
        <div className="composer-row">
          <Segmented label="Task kind" value={draft.kind} onChange={kind => update({ kind })} options={KINDS} />
        </div>
        <ScopePicker files={files.map(f => f.path)} selected={draft.paths} known={known} onAdd={addPath} onRemove={path => update({ paths: draft.paths.filter(p => p !== path) })} />
        <CriteriaEditor criteria={draft.criteria} onChange={criteria => update({ criteria })} />
        {steps ? (
          <p className="xs muted" role="note">
            {ownWorktree ? `Agents fix these ${steps.length} issues one at a time, with a commit for each; one they cannot fix is left out and opens again.` : `Agents fix these ${steps.length} issues one at a time only in a worktree of its own; here they fix them together.`}
          </p>
        ) : null}
        {error ? (
          <div className="composer-error" role="alert">
            {error}
          </div>
        ) : startBlocker ? (
          <p className="xs muted" role="status">
            {startBlocker}
          </p>
        ) : null}
      </div>
      <div className="composer-foot">
        <div className="composer-options">
          <label className="check-label">
            <input type="checkbox" checked={draft.autoRepair} onChange={e => update({ autoRepair: e.target.checked })} />
            Let agents repair failing checks (up to 2 tries)
          </label>
          {worktreePath ? (
            <span className="check-label" title={worktreePath}>
              <GitBranch size={13} aria-hidden /> In the worktree {basename(worktreePath)}
            </span>
          ) : (
            <Tip label={worktreeBlocker || 'A new branch and worktree from HEAD, so this task runs beside the others; merge it from Branches'}>
              <label className="check-label">
                <input type="checkbox" checked={ownWorktree} disabled={Boolean(worktreeBlocker)} onChange={e => update({ worktree: e.target.checked })} />
                Work in its own worktree
              </label>
            </Tip>
          )}
        </div>
        <span className="spacer" />
        <Button variant="ghost" icon={Save} onClick={() => void submit('draft')} disabled={Boolean(busy)}>
          Save draft
        </Button>
        <Tip label={startBlocker || 'Agents write the change, tests, and review it'}>
          <span>
            <Button variant="primary" icon={Play} type="button" onClick={() => void submit('agents')} loading={busy === 'Creating task…' || busy === 'Starting agents…'} disabled={Boolean(startBlocker)}>
              Start agents <Kbd>mod+Enter</Kbd>
            </Button>
          </span>
        </Tip>
      </div>
    </form>
  );
}

function ScopePicker({ files, selected, known, onAdd, onRemove }: { files: string[]; selected: string[]; known: Set<string>; onAdd: (path: string) => void; onRemove: (path: string) => void }) {
  const [q, setQ] = useState('');
  const [cursor, setCursor] = useState(0);
  const results = useMemo(() => (q.trim() ? rankItems(files.filter(f => !selected.includes(f)), q, f => f, 8) : []), [files, q, selected]);
  // A typed path no file has yet can join the scope as a new file.
  const created = newFilePath(q);
  const createOption = created && !known.has(created) && !selected.includes(created) ? created : '';
  const options = [...results.map(r => r.item), ...(createOption ? [createOption] : [])];
  const hasTest = selected.some(isTestPath);
  const suggestion = !hasTest ? selected.map(companionTestPath).find(Boolean) : '';

  function pick(index: number) {
    const path = options[index];
    if (!path) return;
    onAdd(path);
    setQ('');
    setCursor(0);
  }

  return (
    <div className="scope">
      <div className="scope-head">
        <span className="field-label">Files agents may change</span>
        <span className="xs faint">{plural(selected.length, 'file')} of {MAX_TASK_PATHS}</span>
      </div>
      {selected.length ? (
        <ul className="scope-list">
          {selected.map(path => (
            <li key={path} className="scope-item">
              <FileIcon path={path} />
              <span className="truncate">
                <span className="strong">{basename(path)}</span> <span className="faint">{dirname(path)}</span>
              </span>
              {!known.has(path) ? <span className="badge badge-info">new file</span> : null}
              {isTestPath(path) ? <span className="badge badge-success">test</span> : null}
              <button type="button" className="icon-btn icon-btn-sm" aria-label={`Remove ${path}`} onClick={() => onRemove(path)}>
                <X size={13} aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="scope-search">
        <FileSearch size={14} className="input-icon" aria-hidden />
        <input
          className="input has-icon"
          placeholder="Search files to add, or type a new file's path…"
          value={q}
          onChange={e => {
            setQ(e.target.value);
            setCursor(0);
          }}
          onKeyDown={e => {
            if (e.key === 'ArrowDown') {
              e.preventDefault();
              setCursor(c => Math.min(c + 1, options.length - 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setCursor(c => Math.max(c - 1, 0));
            } else if (e.key === 'Enter' && options.length) {
              e.preventDefault();
              pick(cursor);
            }
          }}
          aria-label="Search files to add"
          aria-controls="scope-results"
          aria-activedescendant={options.length ? `scope-r-${cursor}` : undefined}
        />
        {options.length ? (
          <ul className="scope-results" id="scope-results" role="listbox">
            {results.map((r, i) => (
              <li key={r.item} id={`scope-r-${i}`} role="option" aria-selected={i === cursor} className={cx('scope-result', i === cursor && 'is-active')} onMouseDown={e => e.preventDefault()} onClick={() => pick(i)}>
                <FileIcon path={r.item} />
                <span className="truncate">
                  <Highlight text={r.item} positions={r.positions} />
                </span>
                {companionTestPath(r.item) ? <span className="xs faint">+ test</span> : null}
              </li>
            ))}
            {createOption ? (
              <li id={`scope-r-${results.length}`} role="option" aria-selected={results.length === cursor} className={cx('scope-result', results.length === cursor && 'is-active')} onMouseDown={e => e.preventDefault()} onClick={() => pick(results.length)}>
                <FilePlus size={14} aria-hidden />
                <span className="truncate">
                  New file <span className="mono">{createOption}</span>
                </span>
              </li>
            ) : null}
          </ul>
        ) : null}
      </div>
      {suggestion ? (
        <button type="button" className="scope-suggest" onClick={() => onAdd(suggestion)}>
          <FlaskConical size={13} aria-hidden /> Add <span className="mono">{suggestion}</span> so agents can write tests
        </button>
      ) : null}
    </div>
  );
}

function CriteriaEditor({ criteria, onChange }: { criteria: string[]; onChange: (criteria: string[]) => void }) {
  const [text, setText] = useState('');
  const add = () => {
    const value = text.trim();
    if (!value) return;
    onChange([...criteria, value]);
    setText('');
  };
  return (
    <div className="criteria">
      <span className="field-label">Done when</span>
      {criteria.length ? (
        <ol className="criteria-list">
          {criteria.map((c, i) => (
            <li key={`${i}-${c}`}>
              <span className="grow">{c}</span>
              <button type="button" className="icon-btn icon-btn-sm" aria-label={`Remove criterion ${i + 1}`} onClick={() => onChange(criteria.filter((_, k) => k !== i))}>
                <X size={13} aria-hidden />
              </button>
            </li>
          ))}
        </ol>
      ) : null}
      <div className="row">
        <input
          className="input"
          placeholder={criteria.length ? 'Add another criterion…' : 'e.g. Empty input returns a validation error'}
          value={text}
          onChange={e => setText(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter') {
              e.preventDefault();
              add();
            }
          }}
          aria-label="Acceptance criterion"
        />
        <Button size="sm" icon={text.trim() ? ArrowUp : Plus} onClick={add} disabled={!text.trim()}>
          Add
        </Button>
      </div>
    </div>
  );
}
