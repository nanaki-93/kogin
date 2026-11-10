// The code a guide's step stops at: its file and line, the declaration's
// first line always on the page, and the rest of the declaration behind one
// click. Read from the file as it is now, never from a model.

import { useEffect, useMemo, useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { useFile } from '@/api/hooks';
import type { WalkthroughStep } from '@/api/types';
import { declarationExcerpt } from '@/lib/docs';
import { highlightLines } from '@/lib/highlight';
import { navigate } from '@/lib/router';
import { languageOf, plural } from '@/lib/util';
import { cx } from '@/components/ui';
import { FileLink } from './GuidePages';

/** The most lines of one declaration a step shows; the file opens in Code for the rest. */
const MAX_LINES = 40;

export function StepCode({ step, open: openAll }: { step: WalkthroughStep; open: boolean }) {
  const [open, setOpen] = useState(openAll);
  useEffect(() => setOpen(openAll), [openAll]);
  const file = useFile(step.line ? step.path : undefined);
  const excerpt = useMemo(() => (file.data?.content && step.line ? declarationExcerpt(file.data.content, step.line, MAX_LINES) : null), [file.data, step.line]);
  const html = useMemo(() => (excerpt ? highlightLines(excerpt.lines.join('\n'), languageOf(step.path)) : []), [excerpt, step.path]);
  if (!excerpt) {
    return <div className="row-wrap"><FileLink path={step.path} line={step.line} />{step.symbol ? <code className="xs">{step.symbol}</code> : null}</div>;
  }
  const body = html.length - 1;
  const shown = open ? html : html.slice(0, 1);
  return (
    <figure className="plan-code step-code">
      <figcaption>
        <FileLink path={step.path} line={step.line} />
        <span className="spacer" />
        {body > 0 ? (
          <button type="button" className="link-btn xs step-code-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
            <ChevronRight size={12} className={cx('chev', open && 'is-open')} aria-hidden />
            {open ? 'Fold' : `Show ${step.symbol ?? 'it'} (${plural(body + 1 + excerpt.more, 'line')})`}
          </button>
        ) : null}
      </figcaption>
      <pre className="plan-code-lines" aria-label={`${step.symbol ?? step.path} in ${step.path}`}>
        {shown.map((line, index) => (
          <span key={index} className="plan-code-line">
            <span className="ln">{excerpt.first + index}</span>
            <span className="code-text" dangerouslySetInnerHTML={{ __html: line || ' ' }} />
          </span>
        ))}
      </pre>
      {open && excerpt.more > 0 ? (
        <button type="button" className="link-btn xs step-code-more" onClick={() => navigate('/code', { file: step.path, line: String(excerpt.first + html.length) })}>
          {plural(excerpt.more, 'more line')} in Code
        </button>
      ) : null}
    </figure>
  );
}
