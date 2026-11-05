import { describe, expect, it } from 'vitest';
import type { Finding } from '@/api/types';
import { draftFromRequest, matchIssue, namedFiles } from './askDraft';

const finding = (id: string, title: string, path: string, message = ''): Finding => ({ id, source: 'semantic', severity: 'medium', title, message, location: { path }, status: 'open', task_spec: { acceptance_criteria: ['A canceled context ends the retries'] } });
const issues = [finding('f1', 'Request body read without a size limit', 'internal/api/handlers/change_handler.go', 'io.ReadAll on r.Body.'), finding('f2', 'Retry loop ignores cancellation', 'internal/llm/client.go', 'The backoff loop never checks ctx.')];
const paths = ['internal/llm/client.go', 'internal/llm/client_test.go', 'cmd/daemon/main.go'];

describe('a task from a sentence', () => {
  it('matches the open issue the sentence describes', () => {
    expect(matchIssue('the retry loop in the llm client ignores cancellation', issues)?.id).toBe('f2');
    expect(matchIssue('make the logo bigger', issues)).toBeUndefined();
  });

  it('takes the issue file with its test, its title and criteria', () => {
    const draft = draftFromRequest('> retry loop in the llm client ignores cancellation, fix it', issues, paths);
    expect(draft.issue?.id).toBe('f2');
    expect(draft.title).toBe('Retry loop ignores cancellation');
    expect(draft.paths).toEqual(['internal/llm/client.go', 'internal/llm/client_test.go']);
    expect(draft.criteria).toEqual(['A canceled context ends the retries']);
    expect(draft.kind).toBe('fix');
  });

  it('uses the files a sentence names, and reads a feature', () => {
    expect(namedFiles('log the port in main.go please', paths)).toEqual(['cmd/daemon/main.go']);
    const draft = draftFromRequest('> add a --port flag to cmd/daemon/main.go', issues, paths);
    expect(draft).toMatchObject({ kind: 'feature', title: 'Add a --port flag to cmd/daemon/main.go', paths: ['cmd/daemon/main.go'], criteria: [] });
  });
});
