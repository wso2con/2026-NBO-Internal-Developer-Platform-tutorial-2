import { useEffect, useState } from 'react'
import type { Role, SettlementStatus } from './types'
import { apiGet, devToken } from './api'
import { StalenessBanner } from './components/Banner'
import { CollectionsToday } from './screens/CollectionsToday'
import { TransactionSearch } from './screens/TransactionSearch'
import { SettlementRuns } from './screens/SettlementRuns'
import { Statements } from './screens/Statements'
import { Settings } from './screens/Settings'
import { RunHealth } from './screens/RunHealth'
import { DemoPayment } from './screens/DemoPayment'

type Screen = 'S-1' | 'S-2' | 'S-3' | 'S-4' | 'S-5' | 'S-6' | 'S-7'

// Dev-mode role picker (README.md: no OIDC, no login screens, no session management).
const IDENTITIES: { label: string; sub: string; merchantId: string; role: Role }[] = [
  { label: 'Merchant — Nakuru Fresh Produce (KE)', sub: 'user_ke', merchantId: 'mch_001', role: 'merchant' },
  { label: 'Merchant — Thika Road Hardware (KE, near float limit)', sub: 'user_ke2', merchantId: 'mch_004', role: 'merchant' },
  { label: 'Merchant — Lekki Power Distribution (NG)', sub: 'user_ng', merchantId: 'mch_008', role: 'merchant' },
  { label: 'Merchant — Port Harcourt Logistics (NG, near float limit)', sub: 'user_ng2', merchantId: 'mch_011', role: 'merchant' },
  { label: 'Finance — mopay', sub: 'user_fin', merchantId: '', role: 'finance' },
  { label: 'Operations — mopay', sub: 'user_ops', merchantId: '', role: 'operations' },
]

const SCREENS: { id: Screen; label: string; roles: Role[] }[] = [
  { id: 'S-1', label: 'Collections today', roles: ['merchant'] },
  { id: 'S-2', label: 'Transactions', roles: ['merchant'] },
  { id: 'S-3', label: 'Settlement runs', roles: ['merchant'] },
  { id: 'S-4', label: 'Statements', roles: ['merchant', 'finance'] },
  { id: 'S-5', label: 'Settings', roles: ['merchant'] },
  { id: 'S-6', label: 'Run health', roles: ['operations'] },
  // Staff-only: a stand-in for the payment channel, so a collection can be raised on
  // stage without curl. Merchants have no use for it and do not see it.
  { id: 'S-7', label: 'Demo a payment', roles: ['finance', 'operations'] },
]

export default function App() {
  const [identityIdx, setIdentityIdx] = useState(0)
  const identity = IDENTITIES[identityIdx]
  const token = devToken(identity.sub, identity.merchantId, identity.role)

  const visible = SCREENS.filter((s) => s.roles.includes(identity.role))
  const [screen, setScreen] = useState<Screen>('S-1')

  // Keep the selected screen valid when the role changes.
  useEffect(() => {
    if (!visible.some((s) => s.id === screen)) setScreen(visible[0]?.id ?? 'S-1')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identityIdx])

  // S-3.4: the banner is driven by a continuously-updated signal from collections-api,
  // which computes settlement lag independently of settlement-worker (constraint 4).
  const [status, setStatus] = useState<SettlementStatus | null>(null)
  const [dismissedOn, setDismissedOn] = useState<Screen | null>(null)

  useEffect(() => {
    let stop = false
    const poll = () => {
      apiGet<SettlementStatus>('/v1/settlement-status', token)
        .then((s) => { if (!stop) setStatus(s) })
        .catch(() => { /* the banner is a safety signal; never break a screen over it */ })
    }
    poll()
    const t = setInterval(poll, 15000)
    return () => { stop = true; clearInterval(t) }
  }, [token])

  // S-3.4: "dismissible per session but reappears on navigation." Dismissal is therefore
  // scoped to the screen it was dismissed on, and is not persisted anywhere.
  const dismissed = dismissedOn === screen

  const go = (s: Screen) => { setScreen(s); setDismissedOn(null) }

  return (
    <div className="app">
      <header className="top">
        <div>
          <div className="brand">mo<span>pay</span></div>
          <div className="muted" style={{ fontSize: 12 }}>
            {status ? `data region ${status.region}` : 'merchant console'}
          </div>
        </div>
        <div className="row" style={{ margin: 0 }}>
          {status && <span className="region-chip">data region {status.region}</span>}
          <select value={identityIdx} onChange={(e) => { setIdentityIdx(Number(e.target.value)); setDismissedOn(null) }}>
            {IDENTITIES.map((i, idx) => <option key={i.sub} value={idx}>{i.label}</option>)}
          </select>
        </div>
      </header>

      <nav className="tabs">
        {visible.map((s) => (
          <button key={s.id} aria-current={screen === s.id ? 'page' : undefined} onClick={() => go(s.id)}>
            {s.label}
          </button>
        ))}
      </nav>

      {/* Rendered on every screen, above the body, in the flow. Not a toast. */}
      <StalenessBanner status={status} dismissed={dismissed} onDismiss={() => setDismissedOn(screen)} />

      {screen === 'S-1' && <CollectionsToday token={token} />}
      {screen === 'S-2' && <TransactionSearch token={token} />}
      {screen === 'S-3' && <SettlementRuns token={token} />}
      {screen === 'S-4' && <Statements token={token} merchantId={identity.role === 'finance' ? 'mch_001' : ''} />}
      {screen === 'S-5' && <Settings token={token} />}
      {screen === 'S-6' && <RunHealth token={token} />}
      {screen === 'S-7' && <DemoPayment token={token} />}
    </div>
  )
}
