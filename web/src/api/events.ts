// Live updates. The daemon streams `update` events whose kind names what
// changed; each kind invalidates the queries that depend on it, and workflow
// events name their task so only it reloads. A finished run is announced. The
// stream is read with fetch so it carries the Web UI header like every request.

import { useEffect, useRef, useState } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import type { DaemonEvent } from './types';
import { useFeedback } from '@/components/overlays';
import { costLabel, usageLabel } from '@/lib/usage';

export type Connection = 'connecting' | 'live' | 'offline';

const INVALIDATES: Record<string, string[][]> = {
  // Task checks open refactor issues for files already over their budgets.
  workflow: [['session'], ['changes'], ['git'], ['usage'], ['quarantine'], ['findings'], ['changeRules']],
  project_index: [['project'], ['index'], ['changes'], ['session'], ['overview'], ['trust'], ['git'], ['file'], ['fileSummary'], ['docsComponent'], ['usage'], ['rules'], ['changeRules']],
  findings: [['findings'], ['overview']],
  analysis: [['overview'], ['findings'], ['fileSummary'], ['docsComponent'], ['usage']],
  docs: [['overview'], ['docsComponent']],
  agent_settings: [['settings']],
  configuration: [['setup'], ['models'], ['settings'], ['overview']],
  agent_run: [['session'], ['usage'], ['quarantine']],
};

export function applyEvent(client: QueryClient, event: DaemonEvent): void {
  for (const key of INVALIDATES[event.kind] ?? []) {
    const scoped = key[0] === 'session' && event.kind === 'workflow' && event.session_id ? ['session', event.session_id] : key;
    void client.invalidateQueries({ queryKey: scoped });
  }
}

const FINISHED: Record<string, { message: string; tone: 'success' | 'warning' | 'danger' | 'info' }> = {
  awaiting_human_review: { message: 'Agents finished “%”. It needs your review.', tone: 'success' },
  changes_requested: { message: 'Reviewers requested changes on “%”.', tone: 'warning' },
  failed: { message: 'The agent run on “%” failed.', tone: 'danger' },
  stale: { message: '“%” is out of date; its run stopped.', tone: 'warning' },
  paused: { message: 'Agents paused on “%”: a subscription plan reached its usage limit.', tone: 'warning' },
};

/** The announcement for a workflow run that just ended, if it deserves one. */
export function finishedRun(event: DaemonEvent): { message: string; detail: string; tone: 'success' | 'warning' | 'danger' | 'info' } | null {
  const finished = event.kind === 'workflow' && !event.stage ? FINISHED[event.status ?? ''] : undefined;
  if (!finished) return null;
  const detail = [usageLabel(event.usage), costLabel(event.cost)].filter(Boolean).join(' · ');
  return { message: finished.message.replace('%', event.title || 'a task'), detail, tone: finished.tone };
}

/** Parses complete SSE frames from `buffer`; returns the unparsed tail. */
export function parseFrames(buffer: string, onEvent: (event: string, data: string) => void): string {
  const frames = buffer.split('\n\n');
  const tail = frames.pop() ?? '';
  for (const frame of frames) {
    let event = 'message';
    const data: string[] = [];
    for (const line of frame.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim();
      else if (line.startsWith('data:')) data.push(line.slice(5).trim());
    }
    if (data.length) onEvent(event, data.join('\n'));
  }
  return tail;
}

export function useDaemonEvents(): Connection {
  const client = useQueryClient();
  const { toast } = useFeedback();
  const announce = useRef(toast);
  announce.current = toast;
  const [connection, setConnection] = useState<Connection>('connecting');
  useEffect(() => {
    let stopped = false;
    let controller: AbortController | null = null;
    let retry = 1000;
    async function connect() {
      while (!stopped) {
        controller = new AbortController();
        try {
          const response = await fetch('/api/projects/current/events', {
            headers: { Accept: 'text/event-stream', 'X-Mini-Orca-Web-UI': '1' },
            signal: controller.signal,
          });
          if (!response.ok || !response.body) throw new Error('events unavailable');
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = '';
          setConnection('live');
          retry = 1000;
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            buffer = parseFrames(buffer + decoder.decode(value, { stream: true }), (event, data) => {
              if (event !== 'update') return;
              let parsed: DaemonEvent;
              try {
                parsed = JSON.parse(data) as DaemonEvent;
              } catch {
                return; // A malformed frame carries no actionable kind.
              }
              applyEvent(client, parsed);
              const ended = finishedRun(parsed);
              if (ended) announce.current(ended.message, ended.tone, ended.detail || undefined);
            });
          }
        } catch {
          if (stopped) return;
        }
        if (stopped) return;
        setConnection('offline');
        await new Promise(resolve => setTimeout(resolve, retry));
        retry = Math.min(15_000, retry * 2);
        setConnection('connecting');
      }
    }
    void connect();
    return () => {
      stopped = true;
      controller?.abort();
    };
  }, [client]);
  return connection;
}
