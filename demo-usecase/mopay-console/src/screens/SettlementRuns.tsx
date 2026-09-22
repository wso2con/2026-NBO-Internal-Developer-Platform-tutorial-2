import type { SettlementRun } from '../types'
import { money, duration, when } from '../api'
import { useResource } from '../useResource'
import { Resource, Pill } from '../components/States'

interface RunList { items: SettlementRun[]; windowSeconds: number }

/** Elapsed time for a run, against NFR-3's two-hour window (S-3.3). */
export function elapsedSeconds(r: SettlementRun): number {
  const end = r.endedAt ? new Date(r.endedAt).getTime() : Date.now()
  return (end - new Date(r.startedAt).getTime()) / 1000
}

/** S-3 — Settlement runs: date, type, status, row count, net amount. */
export function SettlementRuns({ token }: { token: string }) {
  const { data, error, loading, reload } = useResource<RunList>('/v1/settlement-runs?limit=40', token)

  return (
    <div className="card">
      <h2>Settlement runs</h2>
      <p className="sub">Your settlement history. Runs are scheduled nightly, with a reconciliation run at month end.</p>

      <Resource
        what="settlement runs" data={data} error={error} loading={loading} onRetry={reload}
        isEmpty={(d) => d.items.length === 0}
        emptyTitle="No settlement runs yet"
        emptyDetail="Your first run appears after the next nightly settlement."
      >
        {(d) => (
          <table>
            <thead>
              <tr>
                <th>Started</th><th>Type</th><th>Status</th>
                <th className="num">Rows</th><th className="num">Net</th><th className="num">Elapsed</th>
              </tr>
            </thead>
            <tbody>
              {d.items.map((r) => {
                const el = elapsedSeconds(r)
                // S-3.3: a running run shows elapsed against the window, and one that
                // has exceeded it is visually distinguished.
                const over = el > d.windowSeconds
                return (
                  <tr key={r.id}>
                    <td>{when(r.startedAt)}</td>
                    <td className="mono">{r.runType}</td>
                    <td>
                      {/* S-3.1: exactly one of pending, running, completed, failed. */}
                      <Pill status={r.status} />
                      {/* S-3.2: a failed run shows its reason inline, without a click. */}
                      {r.status === 'failed' && r.failureReason && (
                        <div className="reason">{r.failureReason}</div>
                      )}
                    </td>
                    <td className="num">{r.rowCount.toLocaleString('en-US')}</td>
                    <td className="num">{r.netMinor !== undefined ? money(r.netMinor, r.currency) : '—'}</td>
                    <td className={`num${over ? ' over' : ''}`}>
                      {duration(el)}
                      {over && <div className="reason">exceeded {duration(d.windowSeconds)} window</div>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Resource>
    </div>
  )
}
