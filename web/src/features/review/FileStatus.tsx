import type { FileChange } from '@/api/types';

/** Says when a file is new, deleted or moved; a modified file needs no label. */
export function FileStatus({ change }: { change: FileChange }) {
  if (change.status === 'added') return <span className="badge badge-success">new</span>;
  if (change.status === 'deleted') return <span className="badge badge-danger">deleted</span>;
  if (change.status === 'renamed') return <span className="badge badge-info" title={`Renamed from ${change.old_path}`}>renamed from {change.old_path}</span>;
  return null;
}
