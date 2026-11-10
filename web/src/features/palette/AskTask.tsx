// The palette's request mode: "> retry loop in the llm client ignores
// cancellation" drafts a task (title, files, done-when, the open issue it
// matches) and offers to start the agents on it, or to open it in the
// composer first. Nothing runs until one is chosen.

import { useMemo } from 'react';
import { Command } from 'cmdk';
import { CircleAlert, Map as MapIcon, PencilLine, Play, Sparkles } from 'lucide-react';
import { useFindings, useGitWorkspace, useIndex, useMainProject, useMainTrust, useProfile } from '@/api/hooks';
import { useActions } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { openNewTask } from '@/app/store';
import { draftFromRequest } from '@/lib/askDraft';
import { composerStartBlocker, ownWorktreeBlocker } from '@/lib/workflow';
import { isCountedOpenFinding } from '@/components/domain';
import { Kbd } from '@/components/ui';

export function AskTask({ text, close }: { text: string; close: () => void }) {
  const findings = useFindings().data?.findings;
  const files = useIndex().data?.files;
  const project = useMainProject().data;
  const trust = useMainTrust().data;
  const git = useGitWorkspace().data;
  const gate = useProfile().data?.plan_gate ?? 'always';
  const investigates = gate === 'always';
  const { worktreePath } = useCheckout();
  const actions = useActions();
  const request = text.replace(/^\s*>\s*/, '');
  const draft = useMemo(() => draftFromRequest(request, (findings ?? []).filter(isCountedOpenFinding), (files ?? []).map(file => file.path)), [request, findings, files]);
  const blocker = (git ? ownWorktreeBlocker(git, Boolean(trust?.trusted)) : '') || composerStartBlocker(draft.paths, Boolean(trust?.trusted), investigates);

  if (request.trim().split(/\s+/).length < 2) {
    return (
      <Command.Group heading="New task from a sentence">
        <Command.Item value="ask-hint" disabled className="palette-item">
          <Sparkles size={15} aria-hidden />
          <span className="grow muted">Describe the change, such as “retry loop in the llm client ignores cancellation”</span>
        </Command.Item>
      </Command.Group>
    );
  }

  const base = { title: draft.title, kind: draft.kind, description: draft.description, paths: draft.paths, criteria: draft.criteria };
  async function start() {
    close();
    if (!project) return;
    const fromIssue = draft.issue ? await actions.issueTaskDraft(project, worktreePath, draft.issue) : null;
    // Creating the task opens it, and the run starts there.
    await actions.createTask(project, { ...(fromIssue ?? base), description: fromIssue ? `${request}\n\n${fromIssue.description}` : base.description, start: 'agents' });
  }
  function edit() {
    close();
    if (draft.issue) void actions.fixIssue(project, worktreePath, draft.issue);
    else openNewTask({ ...base });
  }

  return (
    <>
      <div className="ask-task" aria-live="polite">
        <div className="row xs muted"><Sparkles size={13} aria-hidden /> The task Kogin would start from what you typed. Nothing runs until you choose.</div>
        <div className="ask-task-card">
          <div className="strong">{draft.title}</div>
          {draft.issue ? (
            <div className="small muted"><CircleAlert size={12} aria-hidden /> Fixes the open issue <b>{draft.issue.title}</b> ({draft.issue.severity}, {draft.issue.location.path}{draft.issue.location.start_line ? `:${draft.issue.location.start_line}` : ''})</div>
          ) : null}
          <div className="ask-task-files">
            {draft.paths.length ? (
              draft.paths.map(path => <span key={path} className="ask-task-file mono">{path}</span>)
            ) : investigates ? (
              <span className="small muted">No file pinned — the investigation will find the files to change.</span>
            ) : (
              <span className="small tone-warning">No file matched: name one (such as client.go), or choose them in the composer.</span>
            )}
          </div>
          {draft.criteria.length ? <div className="small"><span className="muted">Done when:</span> {draft.criteria.join(' · ')}</div> : null}
          <div className="xs faint">{draft.kind === 'feature' ? 'Feature' : 'Fix'} · in its own worktree</div>
        </div>
      </div>
      <Command.Group heading="New task">
        <Command.Item value="ask-start" disabled={Boolean(blocker)} onSelect={() => void start()} className="palette-item">
          {investigates ? <MapIcon size={15} aria-hidden /> : <Play size={15} aria-hidden />}
          <span className="grow">{blocker ? `Start agents: ${blocker}` : investigates ? 'Investigate & plan it' : 'Start agents on it'}</span>
          <Kbd>↵</Kbd>
        </Command.Item>
        <Command.Item value="ask-edit" onSelect={edit} className="palette-item">
          <PencilLine size={15} aria-hidden />
          <span className="grow">Edit it first in the composer</span>
        </Command.Item>
      </Command.Group>
    </>
  );
}
