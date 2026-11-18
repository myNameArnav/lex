import { api } from '../api.js';

// A server restart briefly makes the proxy unavailable: retry with backoff.
// Permission and media errors stay terminal; cancelled() stops the retries.
export async function retryApi(path, opts, { cancelled = () => false, retries = 6, onRetry } = {}) {
  for (let attempt = 0; ; attempt++) {
    try {
      return await api(path, opts);
    } catch (e) {
      if (cancelled() || ![0, 502, 503, 504].includes(e.status) || attempt >= retries) throw e;
      onRetry?.(attempt + 1);
      await new Promise((resolve) => setTimeout(resolve, Math.min(8000, 1000 * (attempt + 1))));
      if (cancelled()) throw e;
    }
  }
}
