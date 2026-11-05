// Issues found and fixed per day: paired bars on one axis, a legend with the
// week's totals, a tooltip per day and a table view.

import { useEffect, useRef, useState } from 'react';
import type { HealthDay } from '@/api/types';

const HEIGHT = 120;
const PAD = { top: 8, right: 4, bottom: 20, left: 28 };

function dayLabel(date: string): string {
  const parsed = new Date(date + 'T00:00:00Z');
  return parsed.toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

/** A round step for the y axis so the top tick reads well. */
function niceMax(value: number): number {
  if (value <= 4) return 4;
  const step = Math.pow(10, Math.floor(Math.log10(value)));
  return Math.ceil(value / step) * step;
}

export function TrendChart({ days }: { days: HealthDay[] }) {
  const [hover, setHover] = useState<number | null>(null);
  // Drawn in CSS pixels at the container's width, so text keeps its size.
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  useEffect(() => {
    const element = box.current;
    if (!element || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(entries => setWidth(Math.max(280, Math.round(entries[0].contentRect.width))));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const plotWidth = width - PAD.left - PAD.right;
  const plotHeight = HEIGHT - PAD.top - PAD.bottom;
  const top = niceMax(Math.max(1, ...days.flatMap(day => [day.found, day.fixed])));
  const slot = plotWidth / Math.max(1, days.length);
  const bar = Math.min(12, (slot - 6) / 2);
  const y = (value: number) => PAD.top + plotHeight - (value / top) * plotHeight;
  const found = days.reduce((sum, day) => sum + day.found, 0);
  const fixed = days.reduce((sum, day) => sum + day.fixed, 0);
  const shown = hover !== null ? days[hover] : null;

  return (
    <div className="trend-chart" ref={box}>
      <div className="trend-legend small" aria-hidden>
        <span><i className="swatch swatch-found" /> Found <span className="muted">{found}</span></span>
        <span><i className="swatch swatch-fixed" /> Fixed <span className="muted">{fixed}</span></span>
        <span className="xs faint">last {days.length} days</span>
        {shown ? <span className="trend-tooltip xs">{dayLabel(shown.date)}: {shown.found} found, {shown.fixed} fixed</span> : null}
      </div>
      <svg width={width} height={HEIGHT} viewBox={`0 0 ${width} ${HEIGHT}`} role="img" aria-label={`Issues found and fixed per day: ${found} found and ${fixed} fixed in ${days.length} days`} className="trend-svg" onMouseLeave={() => setHover(null)}>
        {[0, top / 2, top].map(tick => (
          <g key={tick}>
            <line x1={PAD.left} x2={width - PAD.right} y1={y(tick)} y2={y(tick)} className={tick === 0 ? 'trend-baseline' : 'trend-grid'} />
            <text x={PAD.left - 6} y={y(tick) + 3} textAnchor="end" className="trend-axis">{tick}</text>
          </g>
        ))}
        {days.map((day, i) => {
          const x = PAD.left + i * slot + (slot - 2 * bar - 2) / 2;
          return (
            <g key={day.date} onMouseEnter={() => setHover(i)} className={hover === i ? 'is-hover' : undefined}>
              <rect x={PAD.left + i * slot} y={PAD.top} width={slot} height={plotHeight} className="trend-hit" />
              {day.found ? <path d={barPath(x, y(day.found), bar, y(0))} className="trend-found" /> : null}
              {day.fixed ? <path d={barPath(x + bar + 2, y(day.fixed), bar, y(0))} className="trend-fixed" /> : null}
              {(days.length - 1 - i) % (slot < 44 ? 2 : 1) === 0 ? (
                <text x={PAD.left + i * slot + slot / 2} y={HEIGHT - 6} textAnchor="middle" className="trend-axis">{dayLabel(day.date)}</text>
              ) : null}
            </g>
          );
        })}
      </svg>
      <details className="issue-more">
        <summary className="xs muted">As a table</summary>
        <table className="table" aria-label="Found and fixed per day">
          <thead><tr><th>Day</th><th className="num">Found</th><th className="num">Fixed</th></tr></thead>
          <tbody>{days.map(day => <tr key={day.date}><td>{dayLabel(day.date)}</td><td className="num">{day.found}</td><td className="num">{day.fixed}</td></tr>)}</tbody>
        </table>
      </details>
    </div>
  );
}

/** A bar from the baseline up to top, its data end rounded. */
function barPath(x: number, top: number, width: number, base: number): string {
  const r = Math.min(4, width / 3, base - top);
  return `M${x},${base}V${top + r}Q${x},${top} ${x + r},${top}H${x + width - r}Q${x + width},${top} ${x + width},${top + r}V${base}Z`;
}
