import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import type { AgentActivity, ChangeActivity } from '@/api/types';
import { applyEvent, withActivity } from '@/api/events';

const call = (seq: number, status = 'running'): AgentActivity => ({ seq, at: '2026-11-05T10:00:00Z', role: 'coder', tool: 'read_file', target: 'main.go', status });

describe('agent activity events', () => {
  it('add a call, then update it when it ends', () => {
    let log = withActivity({ workflow_id: 'wf-1', entries: [call(1, 'done')] }, call(2), 'wf-1');
    expect(log.entries.map(entry => entry.seq)).toEqual([1, 2]);
    log = withActivity(log, call(2, 'failed'));
    expect(log.entries.map(entry => entry.status)).toEqual(['done', 'failed']);
  });

  it('start a new log for another workflow', () => {
    expect(withActivity({ workflow_id: 'wf-1', entries: [call(1)] }, call(9), 'wf-2')).toEqual({ workflow_id: 'wf-2', entries: [call(9)] });
  });

  it('extend only a log the page has loaded', () => {
    const client = new QueryClient();
    applyEvent(client, { kind: 'activity', session_id: 'chg-1', workflow_id: 'wf-1', activity: call(1) });
    expect(client.getQueryData(['activity', 'chg-1'])).toBeUndefined();
    client.setQueryData<ChangeActivity>(['activity', 'chg-1'], { workflow_id: 'wf-1', entries: [] });
    applyEvent(client, { kind: 'activity', session_id: 'chg-1', workflow_id: 'wf-1', activity: call(1) });
    expect(client.getQueryData<ChangeActivity>(['activity', 'chg-1'])?.entries).toHaveLength(1);
  });
});
