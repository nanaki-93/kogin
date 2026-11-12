// Pure AST symbol outline and cyclomatic complexity helpers for the Code
// X-Ray workbench and inline task code preview.

import type { FileFunctionMetric, FileOutlineSymbol } from '@/api/types';

export interface EnrichedSymbol {
  kind: string;
  name: string;
  line: number;
  start_line: number;
  end_line: number;
  signature?: string;
  complexity?: number;
  complexityState?: 'ok' | 'near' | 'over';
}

export function symbolKindShort(kind: string): string {
  switch (kind) {
    case 'function':
      return 'fn';
    case 'method':
      return 'mth';
    case 'struct':
      return 'str';
    case 'interface':
      return 'ifc';
    case 'class':
      return 'cls';
    case 'type':
      return 'typ';
    case 'const':
      return 'cst';
    case 'var':
      return 'var';
    default:
      return kind.slice(0, 3) || 'sym';
  }
}

export function complexityState(complexity: number, budget = 15): 'ok' | 'near' | 'over' {
  if (complexity > budget) return 'over';
  if (complexity >= Math.max(8, budget - 3)) return 'near';
  return 'ok';
}

export function mergeOutlineWithComplexity(
  outline: FileOutlineSymbol[] | undefined,
  functions: FileFunctionMetric[] | undefined,
  budget = 15,
): EnrichedSymbol[] {
  if (!outline?.length && !functions?.length) return [];
  const fnByLine = new Map<number, FileFunctionMetric>();
  const fnByName = new Map<string, FileFunctionMetric>();
  for (const fn of functions ?? []) {
    fnByLine.set(fn.start_line, fn);
    fnByName.set(fn.name, fn);
  }
  if (!outline?.length) {
    return (functions ?? []).map(fn => ({
      name: fn.name,
      kind: fn.kind || (fn.name.includes('.') ? 'method' : 'function'),
      line: fn.start_line,
      start_line: fn.start_line,
      end_line: fn.end_line,
      complexity: fn.complexity,
      complexityState: complexityState(fn.complexity, budget),
    }));
  }
  return outline.map(sym => {
    const fn = fnByLine.get(sym.line) ?? fnByName.get(sym.name);
    if (!fn) {
      return {
        ...sym,
        start_line: sym.line,
        end_line: sym.line,
      };
    }
    return {
      ...sym,
      start_line: fn.start_line,
      end_line: fn.end_line,
      complexity: fn.complexity,
      complexityState: complexityState(fn.complexity, budget),
    };
  });
}

/** Innermost AST symbol enclosing a 1-based line number. */
export function enclosingSymbol(symbols: EnrichedSymbol[], line: number): EnrichedSymbol | undefined {
  if (line <= 0 || !symbols.length) return undefined;
  let best: EnrichedSymbol | undefined;
  for (const sym of symbols) {
    if (line >= sym.start_line && line <= sym.end_line) {
      if (!best || sym.end_line - sym.start_line <= best.end_line - best.start_line) {
        best = sym;
      }
    }
  }
  return best;
}
