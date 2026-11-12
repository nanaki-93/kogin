import { describe, expect, it } from 'vitest';
import { seniorHunkChecks, socraticPlanPrompts } from './socraticCoach';

describe('socraticCoach', () => {
  it('tailors study-mode prompts by task kind', () => {
    expect(socraticPlanPrompts('security', ['internal/api/auth.go']).conceptId).toBe('boundary-parse-dont-validate');
    expect(socraticPlanPrompts('performance', ['internal/project/index.go']).conceptId).toBe('amortized-complexity');
    expect(socraticPlanPrompts('fix', ['internal/api/webui.go']).prompts).toHaveLength(3);
  });

  it('surfaces deterministic senior checks on changed production and test hunks', () => {
    const prodChecks = seniorHunkChecks('internal/api/handlers/webui.go', [
      { kind: 'context', old_line: 41, new_line: 41, text: 'name := trim(r)' },
      { kind: 'added', new_line: 42, text: 'if name == "" || strings.Contains(name, "/") {' },
    ]);
    expect(prodChecks).toHaveLength(1);
    expect(prodChecks[0].label).toBe('Canonical edge cases');

    const testChecks = seniorHunkChecks('internal/api/handlers/webui_test.go', [
      { kind: 'added', new_line: 88, text: 'func TestServeAssetRejectsNestedPaths(t *testing.T) {}' },
    ]);
    expect(testChecks).toHaveLength(1);
    expect(testChecks[0].conceptId).toBe('table-driven-tests');
  });
});
