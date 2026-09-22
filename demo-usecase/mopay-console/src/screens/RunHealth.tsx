import type { RunHealth as RunHealthData } from '../types'
import { duration, when } from '../api'
import { useResource } from '../useResource'
import { Resource, Pill } from '../components/States'
import { elapsedSeconds } from './SettlementRuns'

/**
 * S-6 — Run health, for operations: all runs across merchants.
 *
 * There is deliberately no retry control. C-1.15 and S-6's retry action are README.md
 * non-goals (SCOPE.md conflict 4).
 *
 * C-6.3 is enforced server-side: an operations token cannot retrieve per-transaction
 * customer references or amounts through any endpoint, so nothing here can show them.
 */
export function RunHealth({ token }: { token: string }) {
  const { data, error, loading, reload } = useResource<RunHealthData>('/v1/ops/run-health?limit=40', token)

  return (
    <div className="card">
      <h2>Run health</h2>
      <p className="sub">Settlement runs across all merchants in this environment.</p>

      <Resource
        what="run health" data={data} error={error} loading={loading} onRetry={reload}
        isEmpty={(d) => d.runs.length === 0 && !d.neverStarted}
        emptyTitle="No runs in this window"
      >
        {(d) => (
          <>
            <div className="grid" style={{ marginBottom: 18 }}>
              <div className="stat">
                <div className="label">Settlement lag</div>
                <div className={`value small${d.settlementStatus.stale ? ' over' : ''}`}>
                  {duration(d.settlementStatus.lagSeconds)}
                </div>
              </div>
              <div className="stat">
                <div className="label">Unsettled</div>
                <div className="value">{d.settlementStatus.unsettledCount.toLocaleString('en-US')}</div>
              </div>
              <div className="stat">
                <div className="label">Merchants affected</div>
                <div className="value">{d.settlementStatus.affectedMerchants}</div>
              </div>
              <div className="stat">
                <div className="label">Last completed run</div>
                <div className="value small">{when(d.settlementStatus.lastCompletedRunAt)}</div>
              </div>
            </div>

            {/*
              S-6.1: a run that failed is distinguished from one that never started.
              "Never started" is the absence of any run record, not a failure - they are
              different operational conditions with different causes.
            */}
            {d.neverStarted && (
              <div className="state" style={{ border: '1px solid var(--line)', borderRadius: 8, marginBottom: 16 }}>
                <div className="title">No settlement run has ever started</div>
                <div className="detail">
                  This is not a failed run — no run record exists at all. Check that
                  settlement is scheduled in this environment.
                </div>
              </div>
            )}

            <table>
              <thead>
                <tr>
                  <th>Started</th><th>Type</th><th>Region</th><th>Status</th>
                  <th className="num">Rows</th><th className="num">Elapsed</th>
                </tr>
              </thead>
              <tbody>
                {d.runs.map((r) => {
                  const el = elapsedSeconds(r)
                  const over = el > d.windowSeconds
                  return (
                    <tr key={r.id}>
                      <td>{when(r.startedAt)}<div className="mono muted" style={{ fontSize: 11 }}>{r.id}</div></td>
                      <td className="mono">{r.runType}</td>
                      <td>{r.region}</td>
                      <td>
                        <Pill status={r.status} />
                        {r.status === 'failed' && r.failureReason && (
                          <div className="reason">{r.failureReason}</div>
                        )}
                      </td>
                      <td className="num">{r.rowCount.toLocaleString('en-US')}</td>
                      <td className={`num${over ? ' over' : ''}`}>
                        {duration(el)}
                        {over && <div className="reason">exceeded window</div>}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </>
        )}
      </Resource>
    </div>
  )
}
