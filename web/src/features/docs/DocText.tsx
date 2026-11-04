// Model-written docs prose with its names linked to the code: a file opens
// in Code, a component opens its page, a declaration the overview's steps
// found opens in Code at its line. Anything else stays plain text.

import { createContext, useContext, useMemo, type ReactNode } from 'react';
import { useIndex } from '@/api/hooks';
import type { ProjectDocs } from '@/api/types';
import { docTextParts, symbolRefs, type DocRefs } from '@/lib/docs';
import { navigate } from '@/lib/router';

const NO_REFS: DocRefs = { files: new Set(), components: new Set(), symbols: new Map() };
const RefsContext = createContext<DocRefs>(NO_REFS);

/** Gives the docs pages below it what their prose can link to. */
export function DocRefsProvider({ docs, children }: { docs: ProjectDocs; children: ReactNode }) {
  const files = useIndex().data?.files;
  const refs = useMemo<DocRefs>(() => ({
    files: new Set((files ?? []).map(file => file.path)),
    components: new Set((docs.components ?? []).map(component => component.path)),
    symbols: symbolRefs([...(docs.walkthroughs ?? []), ...(docs.change_guide ?? [])]),
  }), [files, docs.components, docs.walkthroughs, docs.change_guide]);
  return <RefsContext.Provider value={refs}>{children}</RefsContext.Provider>;
}

export function DocText({ text }: { text: string }) {
  const refs = useContext(RefsContext);
  const parts = useMemo(() => docTextParts(text, refs), [text, refs]);
  return (
    <>
      {parts.map((part, index) => {
        switch (part.kind) {
          case 'text':
            return <span key={index}>{part.text}</span>;
          case 'code':
            return <code key={index} className="doc-code">{part.text}</code>;
          case 'component':
            return <button key={index} type="button" className="link-btn doc-ref mono" onClick={() => navigate('/docs/components', { path: part.path })} title={`Open the ${part.path} component`}>{part.text}</button>;
          default:
            return (
              <button key={index} type="button" className="link-btn doc-ref mono" title={`Open ${part.path}${part.line ? ` at line ${part.line}` : ''} in Code`}
                onClick={() => navigate('/code', { file: part.path, line: part.line ? String(part.line) : undefined })}>
                {part.text}
              </button>
            );
        }
      })}
    </>
  );
}
