import { describe, expect, it } from 'vitest';
import { complexityState, enclosingSymbol, mergeOutlineWithComplexity, symbolKindShort } from './symbols';

describe('symbols', () => {
  it('merges AST outline declarations with function spans and cyclomatic complexity', () => {
    const merged = mergeOutlineWithComplexity(
      [
        { kind: 'struct', name: 'Server', line: 5, signature: 'type Server struct' },
        { kind: 'method', name: 'Server.Serve', line: 12, signature: 'func (s *Server) Serve() error' },
      ],
      [
        { name: 'Server.Serve', kind: 'method', start_line: 12, end_line: 40, complexity: 16 },
      ],
      15,
    );

    expect(merged).toHaveLength(2);
    expect(merged[0].start_line).toBe(5);
    expect(merged[0].complexity).toBeUndefined();
    expect(merged[1].start_line).toBe(12);
    expect(merged[1].end_line).toBe(40);
    expect(merged[1].complexity).toBe(16);
    expect(merged[1].complexityState).toBe('over');

    expect(enclosingSymbol(merged, 24)?.name).toBe('Server.Serve');
    expect(enclosingSymbol(merged, 2)).toBeUndefined();
  });

  it('classifies complexity states and symbol kind abbreviations', () => {
    expect(complexityState(4, 15)).toBe('ok');
    expect(complexityState(13, 15)).toBe('near');
    expect(complexityState(18, 15)).toBe('over');
    expect(symbolKindShort('interface')).toBe('ifc');
    expect(symbolKindShort('function')).toBe('fn');
  });
});
