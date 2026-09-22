/**
 * Runtime configuration.
 *
 * Constraint 5: no environment-specific values in images. A Vite build inlines
 * `import.meta.env.*` into the bundle, so a URL supplied at build time is baked into the
 * artifact and the same image cannot be promoted between environments. These values
 * therefore arrive at *container start* instead, written to /tmp/config.js by the
 * entrypoint and served by nginx as /config.js.
 *
 * Precedence: runtime config (the deployed case) -> Vite env (`npm run dev` only) ->
 * localhost defaults (a bare `vite preview`).
 */
declare global {
  interface Window {
    __MOPAY_CONFIG__?: {
      apiBaseUrl?: string
      merchantApiBaseUrl?: string
    }
  }
}

function resolve(runtime: string | undefined, dev: string | undefined, fallback: string): string {
  const v = (runtime ?? '').trim() || (dev ?? '').trim() || fallback
  return v.replace(/\/+$/, '')
}

const rt = typeof window === 'undefined' ? undefined : window.__MOPAY_CONFIG__

export const API_BASE_URL = resolve(
  rt?.apiBaseUrl,
  import.meta.env.VITE_API_BASE_URL,
  'http://localhost:8088',
)

export const MERCHANT_API_BASE_URL = resolve(
  rt?.merchantApiBaseUrl,
  import.meta.env.VITE_MERCHANT_API_BASE_URL,
  'http://localhost:8092',
)

export {}
