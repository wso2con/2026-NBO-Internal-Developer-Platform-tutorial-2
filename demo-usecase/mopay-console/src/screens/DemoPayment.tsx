import { useEffect, useState } from 'react'
import type { Collection, MerchantPage, MerchantSummary } from '../types'
import { apiGetFrom, apiPost, devToken, money, MERCHANT_API_BASE } from '../api'

/**
 * S-7 - Demo a payment.
 *
 * A stand-in for the payment channel, so a collection can be raised on stage without
 * reaching for curl. Visible to mopay staff only (finance, operations); merchants have no
 * use for it and do not see it.
 *
 * It submits AS THE SELECTED MERCHANT, and that is not a shortcut - it is the only
 * correct way to do it. C-6.2 takes the merchant from the token and never from the
 * request body, and POST /v1/collections admits only the merchant and service roles, so
 * an operations token cannot raise a collection and should not be able to. Minting a
 * merchant-scoped token here is exactly what a channel integration holds.
 *
 * Removing this screen would not grant anyone a capability they lack: the server-side
 * checks are unchanged and still refuse an operations token (C-6.6).
 */

// C-1.8: the channel determines the currency. It is never sent by the caller, and this
// form does not offer it as a field for that reason.
const CHANNELS = [
  { id: 'mpesa', label: 'M-Pesa (Kenya, KES)', region: 'KE' },
  { id: 'nibss_transfer', label: 'NIBSS transfer (Nigeria, NGN)', region: 'NG' },
]

type Outcome = {
  step: string
  ok: boolean
  status: number
  code?: string
  message?: string
  details?: Record<string, unknown>
  collection?: Collection
}

export function DemoPayment({ token }: { token: string }) {
  // The merchant list comes from merchant-api - a different project, with its own
  // datastore, reached over the network. A merchant onboarded there shows up here without
  // this console knowing anything about mopay's tables.
  const [merchants, setMerchants] = useState<MerchantSummary[] | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const [merchantId, setMerchantId] = useState('')
  const [channel, setChannel] = useState(CHANNELS[0].id)
  const [amountMajor, setAmountMajor] = useState('250.00')
  const [clear, setClear] = useState(true)
  const [busy, setBusy] = useState(false)
  const [outcomes, setOutcomes] = useState<Outcome[]>([])

  const loadMerchants = () => {
    setListError(null)
    // listMerchants is a cross-merchant read, so it needs the staff token this screen
    // already has - finance and operations qualify, a merchant token would get 403.
    apiGetFrom<MerchantPage>(MERCHANT_API_BASE, '/v1/merchants?limit=200', token)
      .then((page) => {
        setMerchants(page.items)
        if (page.items.length > 0) setMerchantId((cur) => cur || page.items[0].id)
      })
      .catch((e: Error) => setListError(e.message))
  }

  useEffect(loadMerchants, [token])

  const merchant = merchants?.find((m) => m.id === merchantId)
  const chan = CHANNELS.find((c) => c.id === channel)!
  const mismatch = merchant ? merchant.dataRegion !== chan.region : false

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setOutcomes([])

    // Money is integer minor units on the wire, always.
    const amountMinor = Math.round(Number(amountMajor) * 100)
    // C-1.3: merchantReference is the idempotency key, so every demo payment needs a
    // fresh one - a repeat would return the original and create nothing.
    const reference = `demo-${Date.now()}`

    // The channel's own token, scoped to this merchant (C-6.2).
    const token = devToken('demo-channel', merchantId, 'merchant')

    const created = await apiPost<Collection>('/v1/collections', token, {
      channel,
      amountMinor,
      merchantReference: reference,
      customerReference: '+254700000000',
    })

    const first: Outcome = {
      step: 'POST /v1/collections',
      ok: created.ok,
      status: created.status,
      code: created.code,
      message: created.message,
      details: created.details,
      collection: created.data,
    }
    const next = [first]

    // The second half of the lifecycle: a channel confirms the money later. Without this
    // the collection sits 'pending' and settlement never sees it.
    if (created.ok && created.data && clear) {
      const cb = await apiPost<{ collection: Collection }>('/v1/channel-callbacks', null, {
        collectionId: created.data.id,
        outcome: 'cleared',
      })
      next.push({
        step: 'POST /v1/channel-callbacks (cleared)',
        ok: cb.ok,
        status: cb.status,
        code: cb.code,
        message: cb.message,
      })
    }

    setOutcomes(next)
    setBusy(false)
  }

  return (
    <>
      <div className="card">
        <h2>Demo a payment</h2>
        <p className="sub">
          Stands in for the payment channel. The request is sent <strong>as the merchant
          you choose</strong>, because C-6.2 takes the merchant from the token and never
          from the request body — an operations token cannot raise a collection, and this
          screen does not change that.
        </p>

        <form className="demo-form" onSubmit={submit}>
          <label>
            <span>Merchant</span>
            <select
              value={merchantId}
              disabled={!merchants || merchants.length === 0}
              onChange={(e) => setMerchantId(e.target.value)}
            >
              {!merchants && <option>Loading from merchant-api…</option>}
              {merchants?.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} ({m.dataRegion}) — limit {money(m.floatLimitMinor, m.floatLimitCurrency)}
                </option>
              ))}
            </select>
          </label>

          <label>
            <span>Channel</span>
            <select value={channel} onChange={(e) => setChannel(e.target.value)}>
              {CHANNELS.map((c) => (
                <option key={c.id} value={c.id}>{c.label}</option>
              ))}
            </select>
          </label>

          <label>
            <span>Amount</span>
            <input
              type="number" min="0.01" step="0.01" value={amountMajor}
              onChange={(e) => setAmountMajor(e.target.value)}
            />
          </label>

          <label className="inline">
            <input type="checkbox" checked={clear} onChange={(e) => setClear(e.target.checked)} />
            <span>Clear it immediately (send the channel callback, so settlement can pick it up)</span>
          </label>

          <div>
            <button type="submit" disabled={busy || !merchant}>
              {busy ? 'Sending…' : 'Send payment'}
            </button>
          </div>
        </form>

        {listError && (
          <p className="sub warn">
            Could not list merchants from merchant-api at {MERCHANT_API_BASE}: {listError}{' '}
            <button type="button" onClick={loadMerchants}>Retry</button>
          </p>
        )}

        {mismatch && merchant && (
          <p className="sub warn">
            {merchant.name} is a {merchant.dataRegion} merchant and {chan.label.split(' (')[0]} is
            a {chan.region} channel. The currency comes from the channel (C-1.8), so this
            will not match the merchant’s float limit currency — useful to demonstrate, but
            expect it to be refused.
          </p>
        )}
      </div>

      {outcomes.length > 0 && (
        <div className="card">
          <h2>What the API said</h2>
          {outcomes.map((o, i) => (
            <div key={i} className="outcome">
              <div className="row" style={{ margin: 0, alignItems: 'baseline', gap: 10 }}>
                <span className={o.ok ? "pill completed" : "pill failed"}>{o.status}</span>
                <span className="mono">{o.step}</span>
              </div>

              {o.ok && o.collection && (
                <table>
                  <tbody>
                    <tr><th>Collection</th><td className="mono">{o.collection.id}</td></tr>
                    <tr><th>Amount</th><td>{money(o.collection.amountMinor, o.collection.currency)}</td></tr>
                    <tr><th>Channel</th><td>{o.collection.channel}</td></tr>
                    <tr><th>Status</th><td>{o.collection.status}</td></tr>
                  </tbody>
                </table>
              )}

              {!o.ok && (
                <>
                  <p className="sub"><span className="mono">{o.code}</span> — {o.message}</p>
                  {o.details && (
                    <pre className="mono details">{JSON.stringify(o.details, null, 2)}</pre>
                  )}
                </>
              )}
            </div>
          ))}
          <p className="sub">
            Open <strong>Transactions</strong> as that merchant to see it, or
            <strong> Settlement runs</strong> once the next run has picked it up.
          </p>
        </div>
      )}
    </>
  )
}
