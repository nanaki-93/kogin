// Time limits: how long a model request or check may take before it stops.
// Settings saves them on their own; setup saves them with the models.

import { useEffect, useState } from 'react';
import { Clock } from 'lucide-react';
import type { ProjectSetup, SetupTimeouts } from '@/api/types';
import { useActions, useBusy } from '@/app/actions';
import { TIMEOUTS, timeoutErrors } from '@/lib/setup';
import { formatDuration } from '@/lib/analysis';
import { formatDurationInput, parseDuration } from '@/lib/duration';
import { Button, Field, Input } from '@/components/ui';

/** One field per time limit, typed as a duration ("2m", "1h 30m") or in seconds. */
export function TimeoutFields({ id, timeouts, onChange }: { id: string; timeouts: SetupTimeouts; onChange: (timeouts: SetupTimeouts) => void }) {
  const errors = timeoutErrors(timeouts);
  return (
    <div className="limit-grid">
      {TIMEOUTS.map(({ key, label, hint }) => (
        <Field key={key} label={label} htmlFor={`${id}-${key}`} error={errors[key] ? `${errors[key]} Type it as 2m, 1h 30m or seconds.` : undefined} hint={`${hint} Now ${formatDuration(timeouts[key])}.`}>
          <DurationInput id={`${id}-${key}`} seconds={timeouts[key]} invalid={Boolean(errors[key])} onChange={seconds => onChange({ ...timeouts, [key]: seconds })} />
        </Field>
      ))}
    </div>
  );
}

/** A duration field: keeps what is typed, reports the seconds it means (NaN while it means none). */
function DurationInput({ id, seconds, invalid, onChange }: { id: string; seconds: number; invalid: boolean; onChange: (seconds: number) => void }) {
  const [text, setText] = useState(() => (Number.isNaN(seconds) ? '' : formatDurationInput(seconds)));
  useEffect(() => {
    if (!Number.isNaN(seconds) && parseDuration(text) !== seconds) setText(formatDurationInput(seconds));
  }, [seconds]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Input
      id={id}
      inputMode="text"
      placeholder="2m"
      value={text}
      invalid={invalid}
      onChange={e => {
        setText(e.target.value);
        onChange(parseDuration(e.target.value));
      }}
      onBlur={() => {
        if (!Number.isNaN(parseDuration(text))) setText(formatDurationInput(parseDuration(text)));
      }}
    />
  );
}

/** Settings → Models & agents: edit and save the time limits alone. */
export function TimeLimits({ setup }: { setup: ProjectSetup }) {
  const actions = useActions();
  const busy = Boolean(useBusy());
  const [draft, setDraft] = useState<SetupTimeouts>(setup.timeouts);
  const changed = TIMEOUTS.some(({ key }) => draft[key] !== setup.timeouts[key]);
  const invalid = Object.keys(timeoutErrors(draft)).length > 0;
  return (
    <section className="card" aria-label="Time limits">
      <div className="card-head">
        <Clock size={15} aria-hidden />
        <span className="strong">Time limits</span>
        <span className="xs muted">A request or check that takes longer stops and is reported as timed out.</span>
      </div>
      <div className="card-body">
        <TimeoutFields id="settings-timeout" timeouts={draft} onChange={setDraft} />
      </div>
      <div className="card-foot row">
        <span className="xs muted grow">Saved in this project’s configuration and applied at once, once no analysis or agent work is running. Agent settings are kept.</span>
        <Button size="sm" variant="ghost" onClick={() => setDraft(setup.timeouts)} disabled={!changed || busy}>Discard</Button>
        <Button size="sm" variant="primary" onClick={() => void actions.saveTimeouts(setup, draft)} disabled={!changed || invalid || busy}>Save time limits</Button>
      </div>
    </section>
  );
}
