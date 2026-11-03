// The Kogin mark: a 3×3 weave, emerald threads across and violet threads
// down, passing over and under each other like a checkerboard. The gaps are
// cut out with a mask, so the mark sits on any surface. Source: brand/logo.

import { useId } from 'react';
import { cx } from '@/components/ui';

const LINES = [22, 44, 66];
const CROSSINGS = LINES.flatMap((y, row) => LINES.map((x, column) => ({ x, y, across: (row + column) % 2 === 0 })));

export function BrandMark({ size = 20, className }: { size?: number; className?: string }) {
  const mask = `kogin-mark-${useId().replace(/[^a-zA-Z0-9_-]/g, '')}`;
  // Below 32 px the gaps widen so the weave still reads.
  const gap = size < 32 ? 4 : 2.5;
  return (
    <svg className={cx('brand-mark', className)} viewBox="14 14 72 72" width={size} height={size} aria-hidden focusable="false">
      <defs>
        <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="100" height="100">
          <rect width="100" height="100" fill="#fff" />
          {CROSSINGS.map(({ x, y, across }) =>
            across ? (
              <g key={`${x}-${y}`}>
                <rect x={x} y={y - gap} width="12" height={gap} fill="#000" />
                <rect x={x} y={y + 12} width="12" height={gap} fill="#000" />
              </g>
            ) : (
              <g key={`${x}-${y}`}>
                <rect x={x - gap} y={y} width={gap} height="12" fill="#000" />
                <rect x={x + 12} y={y} width={gap} height="12" fill="#000" />
              </g>
            ),
          )}
        </mask>
      </defs>
      <g mask={`url(#${mask})`}>
        {LINES.map(y => <rect key={`h${y}`} className="brand-thread-across" x="14" y={y} width="72" height="12" rx="2" />)}
        {LINES.map(x => <rect key={`v${x}`} className="brand-thread-down" x={x} y="14" width="12" height="72" rx="2" />)}
        {CROSSINGS.filter(c => c.across).map(({ x, y }) => <rect key={`o${x}-${y}`} className="brand-thread-across" x={x - 0.5} y={y} width="13" height="12" />)}
      </g>
    </svg>
  );
}
