import type { MerchantView, Balance } from '../types'
import { money } from '../api'
import { useResource } from '../useResource'
import { Resource } from '../components/States'

/** S-5 — Settings: payout account, float limit (read-only), data region. */
export function Settings({ token }: { token: string }) {
  const m = useResource<MerchantView>('/v1/merchants/me', token)
  const b = useResource<Balance>('/v1/merchants/me/balance', token)

  return (
    <>
      <div className="card">
        <h2>Payout account</h2>
        <p className="sub">Where your settlements are paid. Contact support to change these.</p>
        <Resource what="your settings" data={m.data} error={m.error} loading={m.loading} onRetry={m.reload}>
          {(d) => (
            <table>
              <tbody>
                <tr><th>Merchant</th><td>{d.merchant.name} <span className="mono muted">({d.merchant.id})</span></td></tr>
                <tr><th>Bank</th><td>{d.merchant.payoutBank}</td></tr>
                <tr><th>Account name</th><td>{d.merchant.payoutAccountName}</td></tr>
                <tr><th>Account number</th><td className="mono">{d.merchant.payoutAccountNumberMasked}</td></tr>
              </tbody>
            </table>
          )}
        </Resource>
      </div>

      <div className="card">
        <h2>Float limit</h2>
        <p className="sub">
          Read-only. The maximum unsettled balance you may accumulate before further
          collections are refused.
        </p>
        <Resource what="your float limit" data={b.data} error={b.error} loading={b.loading} onRetry={b.reload}>
          {(d) => {
            const used = d.floatLimitMinor > 0 ? d.unsettledBalanceMinor / d.floatLimitMinor : 0
            return (
              <div className="grid">
                <div className="stat">
                  <div className="label">Unsettled balance</div>
                  <div className="value small">{money(d.unsettledBalanceMinor, d.currency)}</div>
                </div>
                <div className="stat">
                  <div className="label">Float limit</div>
                  <div className="value small">{money(d.floatLimitMinor, d.currency)}</div>
                </div>
                <div className="stat">
                  <div className="label">Headroom</div>
                  <div className={`value small${d.headroomMinor <= 0 ? ' over' : ''}`}>
                    {money(d.headroomMinor, d.currency)}
                  </div>
                </div>
                <div className="stat">
                  <div className="label">Used</div>
                  <div className={`value${used >= 0.9 ? ' over' : ''}`}>{(used * 100).toFixed(1)}%</div>
                </div>
              </div>
            )
          }}
        </Resource>
      </div>

      <div className="card">
        <h2>Data region</h2>
        {/*
          S-5.1 / RES-7: the storing country is stated in plain language, and the sentence
          comes from the merchant's actual stored dataRegion - true by construction, not
          by policy. The console does not compute it.
        */}
        <Resource what="your data region" data={m.data} error={m.error} loading={m.loading} onRetry={m.reload}>
          {(d) => (
            <>
              <p style={{ fontSize: 16, margin: '6px 0 10px' }}>{d.dataRegionStatement}</p>
              <p className="muted" style={{ fontSize: 12, margin: 0 }}>
                Region code <span className="mono">{d.dataRegion}</span>. Your transaction data is
                not replicated outside this country.
              </p>
            </>
          )}
        </Resource>
      </div>
    </>
  )
}
