// "Next up": the one thing most worth doing now, on top of the Inbox, so a
// glance answers "what needs me?". A review that is ready comes first, then
// a failed run, a branch ready to merge, and the worst open issue. Enter
// starts it; Skip shows the next one.

import { useState } from 'react';
import { CircleAlert, Eye, GitMerge, ShieldAlert } from 'lucide-react';
import type { Finding, GitWorktree, Project, TaskEntry } from '@/api/types';
import { useActions } from '@/app/actions';
import { useCheckout } from '@/app/checkout';
import { usePageKeys } from '@/app/shortcuts';
import { navigate } from '@/lib/router';
import { taskStatus } from '@/lib/readiness';
import { plural } from '@/lib/util';
import { Button, Kbd } from '@/components/ui';

interface Step {
  key: string;
  icon: typeof Eye;
  tone: 'accent' | 'danger' | 'warning' | 'info';
  eyebrow: string;
  title: string;
  detail: string;
  action: string;
  run: () => void;
}

export function NextUp({ needs, failed, toMerge, issues, project }: { needs: TaskEntry[]; failed: TaskEntry[]; toMerge: GitWorktree[]; issues: Finding[]; project: Project | undefined }) {
  const actions = useActions();
  const { worktreePath } = useCheckout();
  const [skipped, setSkipped] = useState<string[]>([]);
  const steps: Step[] = [
    ...needs.map(task => ({
      key: `review-${task.id}`, icon: Eye, tone: 'accent' as const, eyebrow: taskStatus(task).status === 'ready' ? 'Next up · ready to accept' : 'Next up · review',
      title: `Review “${task.title || task.id}”`, detail: [plural(task.paths?.length ?? 0, 'file'), taskStatus(task).label].filter(Boolean).join(' · '),
      action: 'Start review', run: () => navigate(`/tasks/${task.id}/files`, { wt: task.worktreePath || undefined }),
    })),
    ...failed.map(task => ({
      key: `failed-${task.id}`, icon: ShieldAlert, tone: 'danger' as const, eyebrow: `Next up · ${taskStatus(task).label.toLowerCase()}`,
      title: `See why “${task.title || task.id}” stopped`, detail: task.workflow_reason || taskStatus(task).label,
      action: 'Open the task', run: () => navigate(`/tasks/${task.id}`, { wt: task.worktreePath || undefined }),
    })),
    ...toMerge.map(tree => ({
      key: `merge-${tree.path}`, icon: GitMerge, tone: 'warning' as const, eyebrow: 'Next up · ready to merge',
      title: `Merge ${tree.branch || tree.path}`, detail: (tree.overlap_paths ?? []).length ? plural(tree.overlap_paths!.length, 'overlapping file') : 'No overlapping files',
      action: 'Open Branches', run: () => navigate('/git', { path: tree.path }),
    })),
    ...issues.filter(issue => ['critical', 'high'].includes(issue.severity)).slice(0, 3).map(issue => ({
      key: `issue-${issue.id}`, icon: CircleAlert, tone: 'info' as const, eyebrow: `Next up · ${issue.severity} issue`,
      title: `Fix “${issue.title}”`, detail: `${issue.location.path}${issue.location.start_line ? `:${issue.location.start_line}` : ''}`,
      action: 'Fix with agents', run: () => void actions.fixIssue(project, worktreePath, issue),
    })),
  ];
  const left = steps.filter(step => !skipped.includes(step.key));
  const step = left[0];
  const skip = () => step && setSkipped(list => (left.length > 1 ? [...list, step.key] : []));

  usePageKeys(key => {
    if (!step) return false;
    if (key === 'Enter') step.run();
    else if (key === 's') skip();
    else return false;
    return true;
  });

  if (!step) return null;
  return (
    <section className={`next-up tone-${step.tone}`} aria-label="Next up">
      <span className="next-up-icon"><step.icon size={18} aria-hidden /></span>
      <div className="next-up-text">
        <div className="next-up-eyebrow">{step.eyebrow}{left.length > 1 ? ` · ${left.length} waiting` : ''}</div>
        <div className="next-up-title">{step.title}</div>
        {step.detail ? <div className="next-up-detail truncate">{step.detail}</div> : null}
      </div>
      <div className="next-up-actions">
        {left.length > 1 ? <Button variant="ghost" onClick={skip} kbd="S">Skip</Button> : null}
        <Button variant="primary" onClick={step.run}>{step.action} <Kbd>↵</Kbd></Button>
      </div>
    </section>
  );
}
