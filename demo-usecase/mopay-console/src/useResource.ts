import { useCallback, useEffect, useState } from 'react'
import { apiGet } from './api'

/** Small async-resource hook so every screen gets identical loading/error/retry behaviour. */
export function useResource<T>(path: string | null, token: string, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [loading, setLoading] = useState(false)
  const [nonce, setNonce] = useState(0)

  const reload = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    if (!path) return
    let cancelled = false
    setLoading(true)
    setError(null)

    apiGet<T>(path, token)
      .then((d) => { if (!cancelled) { setData(d); setError(null) } })
      .catch((e) => { if (!cancelled) { setError(e); setData(null) } })
      .finally(() => { if (!cancelled) setLoading(false) })

    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, token, nonce, ...deps])

  return { data, error, loading, reload }
}
