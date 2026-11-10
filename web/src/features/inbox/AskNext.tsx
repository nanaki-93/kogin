// The Inbox starts from what you want: one sentence about the change, and
// the composer opens with the task drafted from it (title, files it names,
// the open issue it matches). Nothing runs until you start it there.

import { useState, type FormEvent } from 'react';
import { CornerDownLeft } from 'lucide-react';
import { useFindings, useIndex } from '@/api/hooks';
import type { Project } from '@/api/types';
import { useActions } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { openNewTask } from '@/app/store';
import { draftFromRequest } from '@/lib/askDraft';
import { isCountedOpenFinding } from '@/components/domain';
import { Button } from '@/components/ui';

export function AskNext({ project }: { project: Project | undefined }) {
  const [text, setText] = useState('');
  const findings = useFindings().data?.findings;
  const files = useIndex().data?.files;
  const actions = useActions();
  const { worktreePath } = useCheckout();
  const ready = text.trim().split(/\s+/).filter(Boolean).length >= 2;

  function draft(event: FormEvent) {
    event.preventDefault();
    if (!ready) return;
    const task = draftFromRequest(text, (findings ?? []).filter(isCountedOpenFinding), (files ?? []).map(file => file.path));
    setText('');
    // A sentence that names an open issue becomes that issue's fix.
    if (task.issue) void actions.fixIssue(project, worktreePath, task.issue);
    else openNewTask({ title: task.title, kind: task.kind, description: task.description, paths: task.paths, criteria: task.criteria });
  }

  return (
    <form className="ask-next" onSubmit={draft} aria-label="Start from what you want">
      <label htmlFor="ask-next-input" className="ask-next-label">
        What should {project?.name || 'this project'} do next?
      </label>
      <div className="ask-next-row">
        <input
          id="ask-next-input"
          className="input ask-next-input"
          value={text}
          onChange={event => setText(event.target.value)}
          placeholder="Describe a change in a sentence, such as “copy the password to the clipboard with --copy”"
          autoComplete="off"
        />
        <Button type="submit" variant="primary" iconRight={CornerDownLeft} disabled={!ready}>
          Draft the task
        </Button>
      </div>
      <p className="xs muted">The composer opens with the task drafted from your sentence. Nothing runs until you start it.</p>
    </form>
  );
}
