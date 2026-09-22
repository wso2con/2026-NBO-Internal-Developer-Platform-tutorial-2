import type { Role } from './types'
import { API_BASE_URL, MERCHANT_API_BASE_URL } from './config'

// C-6.1 is a README.md non-goal (no OIDC). Dev-mode bearer tokens carry sub, merchantId
// and role. Authorization itself is enforced server-side (C-6.2 - C-6.7): removing this
// file's role handling does not expose anything the role may not see.
export function devToken(sub: string, merchantId: string, role: Role): string {
  const payload = JSON.stringify({ sub, merchantId, role })
  const b64 = btoa(payload).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `dev.${b64}`
}

const BASE: string = API_BASE_URL

// merchant-api is a DIFFERENT project with its own datastore. The console calls it
// directly to list merchants, which is why merchant-api has to allow this origin.
export const MERCHANT_API_BASE: string = MERCHANT_API_BASE_URL

/** A GET against an absolute base, for APIs outside collections-api. */
export async function apiGetFrom<T>(base: string, path: string, token: string): Promise<T> {
  let resp: Response
  try {
    resp = await fetch(`${base}${path}`, { headers: { Authorization: `Bearer ${token}` } })
  } catch (e) {
    throw new ApiError(
      `Could not reach ${base}. ${(e as Error).message}`,
      'network_unreachable',
      0,
    )
  }
  if (!resp.ok) {
    let code = `http_${resp.status}`
    let message = `${path} returned HTTP ${resp.status}`
    try {
      const body = await resp.json()
      if (body?.code) code = body.code
      if (body?.message) message = body.message
    } catch { /* non-JSON error body */ }
    throw new ApiError(message, code, resp.status)
  }
  return (await resp.json()) as T
}

/** ApiError carries the server's stable machine-readable code alongside the message. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly code: string,
    readonly status: number,
  ) {
    super(message)
  }
}

export async function apiGet<T>(path: string, token: string): Promise<T> {
  let resp: Response
  try {
    resp = await fetch(`${BASE}${path}`, { headers: { Authorization: `Bearer ${token}` } })
  } catch (e) {
    // S-0.1: an error state must name what failed. "Something went wrong" is not enough.
    throw new ApiError(
      `Could not reach collections-api at ${BASE}. ${(e as Error).message}`,
      'network_unreachable',
      0,
    )
  }

  if (!resp.ok) {
    let code = `http_${resp.status}`
    let message = `${path} returned HTTP ${resp.status}`
    try {
      const body = await resp.json()
      if (body?.code) code = body.code
      if (body?.message) message = body.message
    } catch {
      /* non-JSON error body; keep the status-derived message */
    }
    throw new ApiError(message, code, resp.status)
  }
  return (await resp.json()) as T
}

/** Money is integer minor units on the wire. Formatting happens here and nowhere else. */
export function money(minor: number | undefined, currency: string | undefined): string {
  if (minor === undefined || minor === null) return '—'
  const sign = minor < 0 ? '-' : ''
  const abs = Math.abs(minor)
  const major = Math.floor(abs / 100)
  const frac = String(abs % 100).padStart(2, '0')
  return `${sign}${major.toLocaleString('en-US')}.${frac}${currency ? ' ' + currency : ''}`
}

export function duration(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}

export function when(iso: string | null | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('en-GB', {
    day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit',
  })
}

/**
 * The outcome of a write, without throwing.
 *
 * The demo-payment screen needs the FAILURES as much as the successes - a 409
 * float_limit_exceeded carrying the balance and the limit is the most interesting thing
 * intake does - so this returns the server's body either way instead of raising.
 */
export interface ApiResult<T> {
  ok: boolean
  status: number
  data?: T
  code?: string
  message?: string
  details?: Record<string, unknown>
}

export async function apiPost<T>(
  path: string,
  token: string | null,
  body: unknown,
): Promise<ApiResult<T>> {
  let resp: Response
  try {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    if (token) headers.Authorization = `Bearer ${token}`
    resp = await fetch(`${BASE}${path}`, { method: 'POST', headers, body: JSON.stringify(body) })
  } catch (e) {
    // S-0.1: name what failed.
    return {
      ok: false,
      status: 0,
      code: 'network_unreachable',
      message: `Could not reach collections-api at ${BASE}. ${(e as Error).message}`,
    }
  }

  let parsed: unknown = undefined
  try {
    parsed = await resp.json()
  } catch {
    /* empty or non-JSON body */
  }

  if (!resp.ok) {
    const b = (parsed ?? {}) as { code?: string; message?: string; details?: Record<string, unknown> }
    return {
      ok: false,
      status: resp.status,
      code: b.code ?? `http_${resp.status}`,
      message: b.message ?? `${path} returned HTTP ${resp.status}`,
      details: b.details,
    }
  }
  return { ok: true, status: resp.status, data: parsed as T }
}
