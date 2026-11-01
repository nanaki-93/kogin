// Open or switch the active project by its absolute path.

import { useState } from 'react';
import { FolderOpen } from 'lucide-react';
import { useMainProject } from '@/api/hooks';
import { useActions, useBusy } from '@/app/actions';
import { Button, Field, Input } from '@/components/ui';

export function OpenProject() {
  const project = useMainProject().data;
  const [edited, setPath] = useState<string | null>(null);
  const path = edited ?? project?.path ?? '';
  const actions = useActions();
  const busy = useBusy();
  const value = path.trim();
  return (
    <form
      className="row open-project"
      onSubmit={event => {
        event.preventDefault();
        if (value && value !== project?.path) void actions.openProject(value);
      }}
    >
      <Field label="Project folder" hint="An absolute path on this machine. Kasuri restores its saved data or indexes it the first time." htmlFor="project-path">
        <Input id="project-path" icon={FolderOpen} value={path} onChange={e => setPath(e.target.value)} placeholder="/Users/you/code/project" spellCheck={false} />
      </Field>
      <Button type="submit" variant="primary" disabled={!value || value === project?.path || Boolean(busy)} loading={busy === 'Opening project…'}>
        Open
      </Button>
    </form>
  );
}
