import { useState } from 'react'
import type { Statement } from '../types'
import { money } from '../api'
import { useResource } from '../useResource'
import { Resource } from '../components/States'

function monthOptions(n = 6): string[] {
  const out: string[] = []
  const d = new Date()
  for (let i = 0; i < n; i++) {
    out.push(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`)
    d.setMonth(d.getMonth() - 1)
  }
  return out
}

/**
 * S-4 — Statements.
 *
 * Rendered on screen only. CSV export and file downloads are README.md non-goals
 * (SCOPE.md conflict 3), so there is deliberately no download control here.
 */
export function Statements({ token, merchantId }: { token: string; merchantId: string }) {
  const months = monthOptions()
  const [month, setMonth] = useState(months[0])

  const path = `/v1/statements/${month}` + (merchantId ? `?merchantId=${merchantId}` : '')
  const { data, error, loading, reload } = useResource<Statement>(path, token, [month])

  return (
    <div className="card">
      <h2>Monthly statement</h2>
      <p className="sub">Settlement totals for one calendar month, from the immutable settlement lines.</p>

      <div className="row">
        <select value={month} onChange={(e) => setMonth(e.target.value)}>
          {months.map((m) => <option key={m} value={m}>{m}</option>)}
        </select>
      </div>

      <Resource
        what="the statement" data={data} error={error} loading={loading} onRetry={reload}
        isEmpty={(d) => d.rowCount === 0}
        emptyTitle={`No settled activity in ${month}`}
        emptyDetail="A statement appears once a settlement run has completed for this month."
      >
        {(d) => (
          <>
            <div className="grid">
              <div className="stat">
                <div className="label">Gross</div>
                <div className="value small">{money(d.grossMinor, d.currency)}</div>
              </div>
              <div className="stat">
                <div className="label">Fees</div>
                <div className="value small">{money(d.feesMinor, d.currency)}</div>
              </div>
              <div className="stat">
                <div className="label">Net settled</div>
                <div className="value small">{money(d.netMinor, d.currency)}</div>
              </div>
              <div className="stat">
                <div className="label">Collections</div>
                <div className="value">{d.rowCount.toLocaleString('en-US')}</div>
              </div>
            </div>

            <table style={{ marginTop: 18 }}>
              <thead>
                <tr>
                  <th>Channel</th><th className="num">Count</th>
                  <th className="num">Gross</th><th className="num">Fees</th><th className="num">Net</th>
                </tr>
              </thead>
              <tbody>
                {d.byChannel.map((c) => (
                  <tr key={c.channel}>
                    <td className="mono">{c.channel}</td>
                    <td className="num">{c.count.toLocaleString('en-US')}</td>
                    <td className="num">{money(c.grossMinor, d.currency)}</td>
                    <td className="num">{money(c.feesMinor, d.currency)}</td>
                    <td className="num">{money(c.netMinor, d.currency)}</td>
                  </tr>
                ))}
              </tbody>
            </table>

            <p className="muted" style={{ fontSize: 12, marginTop: 12 }}>
              Covered by {d.runIds.length} settlement run{d.runIds.length === 1 ? '' : 's'}.
            </p>
          </>
        )}
      </Resource>
    </div>
  )
}
