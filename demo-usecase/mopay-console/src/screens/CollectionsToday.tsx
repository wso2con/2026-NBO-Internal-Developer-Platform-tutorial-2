import type { Aggregates } from '../types'
import { money } from '../api'
import { useResource } from '../useResource'
import { Resource } from '../components/States'
import { Sparkline } from '../components/Sparkline'

/** S-1 — Collections today: volume, value, success rate, per-channel split, 24h sparkline. */
export function CollectionsToday({ token }: { token: string }) {
  const from = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
  const to = new Date().toISOString()
  const { data, error, loading, reload } = useResource<Aggregates>(
    `/v1/aggregates?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`, token,
  )

  return (
    <div className="card">
      <h2>Collections today</h2>
      <p className="sub">Rolling 24 hours.</p>
      <Resource
        what="collections" data={data} error={error} loading={loading} onRetry={reload}
        isEmpty={(d) => d.count === 0}
        emptyTitle="No collections in the last 24 hours"
        emptyDetail="Collections appear here as soon as your integration posts them."
      >
        {(d) => (
          <>
            <div className="grid">
              <div className="stat">
                <div className="label">Volume</div>
                <div className="value">{d.count.toLocaleString('en-US')}</div>
              </div>
              <div className="stat">
                <div className="label">Value</div>
                <div className="value small">{money(d.valueMinor, d.currency)}</div>
              </div>
              <div className="stat">
                <div className="label">Success rate</div>
                <div className="value">{(d.successRate * 100).toFixed(1)}%</div>
              </div>
              <div className="stat">
                <div className="label">Failed</div>
                <div className="value">{d.failedCount.toLocaleString('en-US')}</div>
              </div>
            </div>

            <div style={{ marginTop: 18 }}>
              <div className="label muted" style={{ fontSize: 11, marginBottom: 6 }}>
                COLLECTIONS PER HOUR
              </div>
              <Sparkline points={d.hourly.map((h) => ({ hour: h.hour, count: h.count }))} />
            </div>

            <table style={{ marginTop: 18 }}>
              <thead>
                <tr>
                  <th>Channel</th>
                  <th className="num">Count</th>
                  <th className="num">Value</th>
                  <th className="num">Failed</th>
                </tr>
              </thead>
              <tbody>
                {d.byChannel.map((c) => (
                  <tr key={c.channel}>
                    <td className="mono">{c.channel}</td>
                    <td className="num">{c.count.toLocaleString('en-US')}</td>
                    <td className="num">{money(c.valueMinor, d.currency)}</td>
                    <td className="num">{c.failedCount.toLocaleString('en-US')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </Resource>
    </div>
  )
}
