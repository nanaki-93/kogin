// A page's own list (the docs pages, settings sections, the file tree, a
// review's changed files) shown in the sidebar in place of the workspace pages:
// the sidebar "drills in", and ‹ Workspace shows the pages again. With the
// sidebar folded to icons, the list stands beside the icons.

import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { createPortal } from 'react-dom';

export interface SidebarSlot {
  /** Where the page's list renders; null until the sidebar has mounted. */
  slot: HTMLElement | null;
  /** Claims the sidebar for a list titled `title`; returns the release. */
  claim: (title: string) => () => void;
}

export const SidebarSlotContext = createContext<SidebarSlot | null>(null);

export function SidebarPanel({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  const sidebar = useContext(SidebarSlotContext);
  const claim = sidebar?.claim;
  useEffect(() => claim?.(title), [claim, title]);
  const content = (
    <>
      <div className="sidebar-panel-head">
        <span className="sidebar-panel-title truncate">{title}</span>
        {actions ? <span className="sidebar-panel-actions">{actions}</span> : null}
      </div>
      {children}
    </>
  );
  // Outside the shell (component tests) the list renders in place.
  if (!sidebar) return <div className="sidebar-panel-inline">{content}</div>;
  return sidebar.slot ? createPortal(content, sidebar.slot) : null;
}
