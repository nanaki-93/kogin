// Records Kasuri set aside because it could not read them, or skipped
// because a newer version wrote them. Work continues without them.

import { FileWarning } from 'lucide-react';
import type { QuarantinedFile, QuarantineReport } from '@/api/types';
import { useQuarantine } from '@/api/hooks';
import { navigate } from '@/lib/router';
import { plural, timeAgo } from '@/lib/util';
import { Button, Callout } from '@/components/ui';

const KIND_LABELS: Record<string, string> = { agent_journal: 'Agent journal', task: 'Task', analysis_run: 'Analysis progress' };

export function quarantineCount(report: QuarantineReport | undefined): number {
  return (report?.files.length ?? 0) + (report?.skipped.length ?? 0);
}

/** A notice on the Inbox while anything is set aside or skipped. */
export function QuarantineNotice() {
  const count = quarantineCount(useQuarantine().data);
  if (!count) return null;
  return (
    <Callout tone="warning" icon={FileWarning} title={`Kasuri set aside or skipped ${plural(count, 'file')} it could not read`} action={<Button size="sm" onClick={() => navigate('/settings/project')}>Review</Button>}>
      Work continues without them. Review what they held and what to do.
    </Callout>
  );
}

/** The list of set-aside and skipped files with their remedies. */
export function QuarantineList() {
  const report = useQuarantine().data;
  if (!quarantineCount(report)) return null;
  return (
    <section className="card" aria-label="Set-aside files">
      <div className="card-head">
        <FileWarning size={15} aria-hidden />
        <span className="strong">Set-aside and skipped files</span>
      </div>
      <ul className="quarantine-list">
        {[...(report?.skipped ?? []), ...(report?.files ?? [])].map(file => (
          <QuarantineItem key={file.file + (file.quarantined_as ?? '')} file={file} />
        ))}
      </ul>
    </section>
  );
}

function QuarantineItem({ file }: { file: QuarantinedFile }) {
  return (
    <li className="quarantine-item">
      <div className="row">
        <span className="badge">{KIND_LABELS[file.kind] ?? file.kind}</span>
        <span className="mono small truncate" title={file.file}>{file.file}</span>
        <span className="spacer" />
        {file.at ? <span className="xs faint">{timeAgo(file.at)}</span> : <span className="xs muted">skipped</span>}
      </div>
      <p className="small">{file.reason[0]?.toUpperCase() + file.reason.slice(1)}.{file.quarantined_as ? <> Its bytes are in <span className="mono">{file.quarantined_as}</span>.</> : null}</p>
      <p className="xs muted">{file.remedy}</p>
    </li>
  );
}
