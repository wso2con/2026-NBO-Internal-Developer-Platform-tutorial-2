import type { SettlementStatus } from '../types'
import { duration, when } from '../api'

/**
 * S-3.4 — the settlement staleness banner.
 *
 * "When the most recent completed run is older than 26 hours, EVERY screen displays a
 * persistent banner naming the last successful run time and the current lag. The banner
 * is dismissible per session but reappears on navigation. It must not be implemented as
 * a transient toast."
 *
 * Three things follow from that wording and are deliberate here:
 *   - it renders above the screen body on every screen, in the document flow, sticky;
 *   - it never auto-dismisses, so it is not a toast;
 *   - dismissal is held in component state keyed to the current screen, so navigating
 *     brings it back. It is NOT persisted to storage.
 *
 * The threshold is configuration, not a constant: it arrives from collections-api as
 * stalenessThresholdSeconds.
 */
export function StalenessBanner({
  status, dismissed, onDismiss,
}: {
  status: SettlementStatus | null
  dismissed: boolean
  onDismiss: () => void
}) {
  if (!status || !status.stale || dismissed) return null

  const thresholdHours = Math.round(status.stalenessThresholdSeconds / 3600)
  const thresholdLabel =
    status.stalenessThresholdSeconds < 3600
      ? duration(status.stalenessThresholdSeconds)
      : `${thresholdHours}h`

  return (
    <div className="banner" role="alert">
      <div className="body">
        <strong>Settlement is behind schedule.</strong>
        <div className="meta">
          {status.lastCompletedRunAt ? (
            <>
              Last successful run completed <b>{when(status.lastCompletedRunAt)}</b>
              {status.lastCompletedRunId && (
                <> (<span className="mono">{status.lastCompletedRunId}</span>)</>
              )}
              . Current settlement lag is <b>{duration(status.lagSeconds)}</b>, against a
              threshold of {thresholdLabel}.
            </>
          ) : (
            <>
              <b>No settlement run has ever completed</b> in this environment, and{' '}
              {status.unsettledCount.toLocaleString('en-US')} collections are waiting.
              Current lag is <b>{duration(status.lagSeconds)}</b>.
            </>
          )}
          {status.affectedMerchants > 0 && (
            <> {status.affectedMerchants} merchant{status.affectedMerchants === 1 ? '' : 's'} affected.</>
          )}
        </div>
      </div>
      <button onClick={onDismiss}>Dismiss</button>
    </div>
  )
}
