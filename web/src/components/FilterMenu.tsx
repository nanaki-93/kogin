// A compact filter: a button that shows the filter and its value ("Status
// Open ▾") and opens a menu of the choices with their counts. A filter that
// is not at its default also shows as a chip that clears it.

import * as Menu from '@radix-ui/react-dropdown-menu';
import { Check, ChevronDown, X } from 'lucide-react';
import { cx } from './ui';

export interface FilterOption {
  value: string;
  label: string;
  count?: number;
}

export function FilterMenu({ label, value, options, onChange }: { label: string; value: string; options: FilterOption[]; onChange: (value: string) => void }) {
  const current = options.find(option => option.value === value) ?? options[0];
  return (
    <Menu.Root>
      <Menu.Trigger asChild>
        <button type="button" className="filter-menu" aria-label={`${label}: ${current?.label}`}>
          <span className="muted">{label}</span>
          <span className="strong">{current?.label}</span>
          <ChevronDown size={13} aria-hidden />
        </button>
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content className="menu" align="start" sideOffset={4} aria-label={label}>
          <Menu.RadioGroup value={value} onValueChange={onChange}>
            {options.map(option => (
              <Menu.RadioItem key={option.value} value={option.value} className="menu-item">
                <span className="menu-check">
                  <Menu.ItemIndicator>
                    <Check size={13} aria-hidden />
                  </Menu.ItemIndicator>
                </span>
                <span className="grow">{option.label}</span>
                {option.count ? <span className="xs muted">{option.count}</span> : null}
              </Menu.RadioItem>
            ))}
          </Menu.RadioGroup>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}

/** A filter that is on: its value and a button that clears it. */
export function FilterChip({ label, onClear, className }: { label: string; onClear: () => void; className?: string }) {
  return (
    <span className={cx('filter-chip', className)}>
      {label}
      <button type="button" aria-label={`Clear ${label}`} onClick={onClear}>
        <X size={12} aria-hidden />
      </button>
    </span>
  );
}
