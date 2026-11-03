// Which configuration file the daemon read, or a warning that it found none
// and its models are the built-in local defaults.

import { AlertTriangle } from 'lucide-react';
import type { ModelsCurrent } from '@/api/types';
import { Button, Callout } from '@/components/ui';
import { navigate } from '@/lib/router';

/** Names the daemon's configuration file, or warns that the built-in defaults apply. */
export function ConfigSource({ models, action }: { models: ModelsCurrent | undefined; action?: boolean }) {
  if (!models || models.config_file === undefined) return null;
  if (models.config_file) return action ? null : <p className="xs muted">Configuration: <span className="mono">{models.config_file}</span></p>;
  return (
    <Callout tone="warning" icon={AlertTriangle} title="No configuration was found" action={<Button size="sm" onClick={() => navigate('/setup')}>Run setup</Button>}>
      Models use the built-in local defaults (<span className="mono">http://localhost:1234/v1</span>), so requests fail unless a local server answers there. Setup saves this project’s own configuration and applies it at once{action ? '' : <>; a daemon-wide one goes in <span className="mono">~/.config/kogin/config.yaml</span></>}.
    </Callout>
  );
}

