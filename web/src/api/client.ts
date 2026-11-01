// Same-origin daemon client. Every request carries the Web UI marker header the
// daemon's local request policy requires for browser-originated calls, and a
// request that changes state carries the daemon's per-launch write token,
// which the daemon writes into the document.

/** The write token from the document; empty when the daemon did not serve it. */
export function documentWriteToken(doc: Document | undefined = globalThis.document): string {
  const token = doc?.querySelector<HTMLMetaElement>('meta[name="kasuri-write-token"]')?.content ?? '';
  return token.startsWith('__') ? '' : token;
}

export class ApiError extends Error {
  readonly status: number;
  readonly data: unknown;
  constructor(message: string, status: number, data: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }
}

export function withWorktree(path: string, worktreePath?: string): string {
  if (!worktreePath) return path;
  return path + (path.includes('?') ? '&' : '?') + 'worktree_path=' + encodeURIComponent(worktreePath);
}

export function query(params: Record<string, string | number | boolean | undefined | null>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') q.set(key, String(value));
  }
  const text = q.toString();
  return text ? '?' + text : '';
}

type Fetch = typeof fetch;

export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

/** The message to show for a failure; a daemon error adds its detail, so a
 * generic "review the request" still says what failed. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    const detail = (err.data as { detail?: unknown } | null)?.detail;
    if (typeof detail === 'string' && detail && !err.message.includes(detail)) return `${err.message} (${detail})`;
  }
  if (err instanceof Error) return err.message;
  return String(err);
}

export function createApi(fetchImpl: Fetch = (...args) => fetch(...args), writeToken: () => string = () => documentWriteToken()) {
  async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { 'X-Kasuri-Web-UI': '1' };
    const init: RequestInit = { method: options.method ?? 'GET', headers, signal: options.signal };
    const token = init.method === 'GET' ? '' : writeToken();
    if (token) headers['X-Kasuri-Token'] = token;
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    const response = await fetchImpl(path, init);
    if (response.status === 204) return null as T;
    const text = await response.text();
    let data: unknown = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
    }
    if (!response.ok) {
      const body = (data ?? {}) as { user_message?: string; message?: string; error?: string };
      throw new ApiError(body.user_message || body.message || body.error || `HTTP ${response.status}`, response.status, data);
    }
    return data as T;
  }
  return {
    get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
    post: <T>(path: string, body: unknown = {}, signal?: AbortSignal) => request<T>(path, { method: 'POST', body, signal }),
    patch: <T>(path: string, body: unknown) => request<T>(path, { method: 'PATCH', body }),
    del: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
  };
}

export type Api = ReturnType<typeof createApi>;

export const api = createApi();
