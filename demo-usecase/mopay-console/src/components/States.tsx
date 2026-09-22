import type { ReactNode } from 'react'
import { ApiError } from '../api'

// S-0.1: every screen has an explicit empty, loading and error state. These are
// requirements, not visual polish, so they live in one place and every screen uses them.

export function Loading({ what }: { what: string }) {
  return (
    <div className="state">
      <div className="title">Loading {what}…</div>
    </div>
  )
}

export function Empty({ title, detail }: { title: string; detail?: string }) {
  return (
    <div className="state">
      <div className="title">{title}</div>
      {detail && <div className="detail">{detail}</div>}
    </div>
  )
}

/**
 * S-0.1: an error state names what failed and offers a retry. It reports the server's
 * stable error code, so a merchant on the phone to support can read out something
 * actionable instead of "it's broken".
 */
export function Failed({ what, error, onRetry }: { what: string; error: unknown; onRetry: () => void }) {
  const code = error instanceof ApiError ? error.code : 'unexpected_error'
  const message = error instanceof Error ? error.message : String(error)
  return (
    <div className="state">
      <div className="title">Could not load {what}</div>
      <div className="detail">
        <span className="mono">{code}</span> — {message}
      </div>
      <button onClick={onRetry}>Retry</button>
    </div>
  )
}

/** Renders the right state for an async resource, or the data. */
export function Resource<T>({
  what, data, error, loading, onRetry, isEmpty, emptyTitle, emptyDetail, children,
}: {
  what: string
  data: T | null
  error: unknown
  loading: boolean
  onRetry: () => void
  isEmpty?: (d: T) => boolean
  emptyTitle?: string
  emptyDetail?: string
  children: (d: T) => ReactNode
}) {
  if (loading && data === null) return <Loading what={what} />
  if (error) return <Failed what={what} error={error} onRetry={onRetry} />
  if (data === null) return <Loading what={what} />
  if (isEmpty?.(data)) return <Empty title={emptyTitle ?? `No ${what} yet`} detail={emptyDetail} />
  return <>{children(data)}</>
}

export function Pill({ status }: { status: string }) {
  return <span className={`pill ${status}`}>{status}</span>
}
