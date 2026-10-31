// Core UI primitives. Visual styling lives in styles/components.css.

import { formatKeys } from '@/app/shortcuts';
import { forwardRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';
import { CheckCircle2, ChevronDown, CircleDashed, CircleDot, Hourglass, Loader2, XCircle, type LucideIcon } from 'lucide-react';
import type { Tone } from '@/lib/util';
import type { PhaseTone } from '@/lib/readiness';

export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ');
}

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'subtle';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: 'sm' | 'md';
  icon?: LucideIcon;
  iconRight?: LucideIcon;
  loading?: boolean;
  kbd?: string;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', icon: Icon, iconRight: IconRight, loading, kbd, className, children, disabled, type = 'button', ...rest },
  ref,
) {
  return (
    <button ref={ref} type={type} className={cx('btn', `btn-${variant}`, `btn-${size}`, className)} disabled={disabled || loading} aria-busy={loading || undefined} {...rest}>
      {loading ? <Loader2 className="spin" size={14} aria-hidden /> : Icon ? <Icon size={14} aria-hidden /> : null}
      {children ? <span className="btn-label">{children}</span> : null}
      {IconRight ? <IconRight size={14} aria-hidden /> : null}
      {kbd ? <Kbd>{kbd}</Kbd> : null}
    </button>
  );
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: LucideIcon;
  label: string;
  size?: 'sm' | 'md';
  active?: boolean;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton({ icon: Icon, label, size = 'md', active, className, type = 'button', ...rest }, ref) {
  return (
    <Tip label={label}>
      <button ref={ref} type={type} aria-label={label} className={cx('icon-btn', size === 'sm' && 'icon-btn-sm', active && 'is-active', className)} {...rest}>
        <Icon size={size === 'sm' ? 14 : 16} aria-hidden />
      </button>
    </Tip>
  );
});

/** A key cap. String children use shortcut notation ("mod+Enter") and are shown for this platform. */
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="kbd">{typeof children === 'string' ? formatKeys(children).join(' ') : children}</kbd>;
}

export function Badge({ tone = 'neutral', children, icon: Icon, title }: { tone?: Tone; children: ReactNode; icon?: LucideIcon; title?: string }) {
  return (
    <span className={cx('badge', `badge-${tone}`)} title={title}>
      {Icon ? <Icon size={12} aria-hidden /> : null}
      {children}
    </span>
  );
}

export function Dot({ tone = 'neutral', pulse }: { tone?: Tone; pulse?: boolean }) {
  return <span className={cx('dot', `dot-${tone}`, pulse && 'dot-pulse')} aria-hidden />;
}

const PHASE_ICON: Record<PhaseTone, LucideIcon> = { passed: CheckCircle2, failed: XCircle, running: Loader2, waiting: Hourglass, idle: CircleDashed };

export function PhaseIcon({ tone, size = 16, label }: { tone: PhaseTone; size?: number; label?: string }) {
  const Icon = PHASE_ICON[tone];
  return <Icon size={size} className={cx('phase-icon', `phase-${tone}`, tone === 'running' && 'spin')} aria-label={label} aria-hidden={label ? undefined : true} />;
}

export function StatusIcon({ tone, size = 14 }: { tone: Tone; size?: number }) {
  const Icon = tone === 'success' ? CheckCircle2 : tone === 'danger' ? XCircle : tone === 'info' ? Loader2 : CircleDot;
  return <Icon size={size} className={cx('status-icon', `tone-${tone}`, tone === 'info' && 'spin')} aria-hidden />;
}

export function Spinner({ size = 16, label = 'Loading' }: { size?: number; label?: string }) {
  return <Loader2 size={size} className="spin muted" aria-label={label} />;
}

export function Tip({ label, children, side = 'bottom' }: { label: ReactNode; children: ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  return (
    <TooltipPrimitive.Root delayDuration={350}>
      <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content side={side} sideOffset={6} className="tooltip">
          {label}
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { icon?: LucideIcon; invalid?: boolean }>(function Input({ icon: Icon, invalid, className, ...rest }, ref) {
  if (!Icon) return <input ref={ref} className={cx('input', invalid && 'is-invalid', className)} aria-invalid={invalid || undefined} {...rest} />;
  return (
    <span className={cx('input-wrap', className)}>
      <Icon size={14} className="input-icon" aria-hidden />
      <input ref={ref} className={cx('input', 'has-icon', invalid && 'is-invalid')} aria-invalid={invalid || undefined} {...rest} />
    </span>
  );
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean }>(function Select({ invalid, className, children, ...rest }, ref) {
  return (
    <span className={cx('select-wrap', className)}>
      <select ref={ref} className={cx('input', 'select', invalid && 'is-invalid')} aria-invalid={invalid || undefined} {...rest}>
        {children}
      </select>
      <ChevronDown size={14} className="select-icon" aria-hidden />
    </span>
  );
});

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }>(function Textarea({ invalid, className, ...rest }, ref) {
  return <textarea ref={ref} className={cx('input', 'textarea', invalid && 'is-invalid', className)} aria-invalid={invalid || undefined} {...rest} />;
});

export function Field({ label, hint, error, children, htmlFor }: { label: string; hint?: ReactNode; error?: string; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="field">
      <label className="field-label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {error ? <div className="field-error">{error}</div> : hint ? <div className="field-hint">{hint}</div> : null}
    </div>
  );
}

export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: string; icon?: LucideIcon; count?: number }[]; onChange: (value: T) => void; label: string }) {
  return (
    <div className="segmented" role="radiogroup" aria-label={label}>
      {options.map(option => (
        <button key={option.value} type="button" role="radio" aria-checked={option.value === value} className={cx('segmented-item', option.value === value && 'is-active')} onClick={() => onChange(option.value)}>
          {option.icon ? <option.icon size={13} aria-hidden /> : null}
          {option.label}
          {option.count ? <span className="count">{option.count}</span> : null}
        </button>
      ))}
    </div>
  );
}

export function EmptyState({ icon: Icon, title, children, action }: { icon: LucideIcon; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <div className="empty-icon">
        <Icon size={20} aria-hidden />
      </div>
      <div className="empty-title">{title}</div>
      {children ? <div className="empty-body">{children}</div> : null}
      {action ? <div className="empty-action">{action}</div> : null}
    </div>
  );
}

export function Skeleton({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div className={cx('skeleton-block', className)} aria-busy="true" aria-label="Loading">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skeleton" style={{ width: `${40 + ((i * 37) % 55)}%` }} />
      ))}
    </div>
  );
}

export function Callout({ tone = 'neutral', icon: Icon, title, children, action }: { tone?: Tone; icon?: LucideIcon; title?: ReactNode; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className={cx('callout', `callout-${tone}`)} role={tone === 'danger' ? 'alert' : undefined}>
      {Icon ? <Icon size={16} className="callout-icon" aria-hidden /> : null}
      <div className="grow">
        {title ? <div className="callout-title">{title}</div> : null}
        {children ? <div className="callout-body">{children}</div> : null}
      </div>
      {action ? <div className="callout-action">{action}</div> : null}
    </div>
  );
}

export function Progress({ value, tone = 'accent', label }: { value: number; tone?: Tone; label?: string }) {
  const percent = Math.max(0, Math.min(100, Math.round(value)));
  return (
    <div className={cx('progress', `progress-${tone}`)} role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="progress-bar" style={{ width: `${percent}%` }} />
    </div>
  );
}

export function SectionHeader({ title, children, count }: { title: ReactNode; children?: ReactNode; count?: number }) {
  return (
    <div className="section-header">
      <h3>
        {title}
        {count !== undefined ? <span className="count">{count}</span> : null}
      </h3>
      {children ? <div className="row">{children}</div> : null}
    </div>
  );
}

/** A titled group of list rows with its item count. */
export function ListGroup({ title, count, icon: Icon, action, children }: { title: string; count: number; icon?: LucideIcon; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="list-group" aria-label={title}>
      <div className="list-group-head">
        {Icon ? <Icon size={13} aria-hidden /> : null}
        {title}
        <span className="count">{count}</span>
        {action ? <span className="list-group-action">{action}</span> : null}
      </div>
      <div role="list">{children}</div>
    </section>
  );
}

export function Highlight({ text, positions }: { text: string; positions: number[] }) {
  if (!positions.length) return <>{text}</>;
  const set = new Set(positions);
  const out: ReactNode[] = [];
  let run = '';
  let marked = false;
  const flush = (key: number) => {
    if (!run) return;
    out.push(marked ? <mark key={key}>{run}</mark> : run);
    run = '';
  };
  for (let i = 0; i < text.length; i++) {
    const hit = set.has(i);
    if (hit !== marked) {
      flush(i);
      marked = hit;
    }
    run += text[i];
  }
  flush(text.length);
  return <>{out}</>;
}
