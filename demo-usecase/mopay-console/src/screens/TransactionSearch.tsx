import { useState } from 'react'
import type { Collection, LedgerEntry } from '../types'
import { money, when } from '../api'
import { useResource } from '../useResource'
import { Resource, Pill } from '../components/States'

interface SearchResult { items: Collection[]; total: number; limit: number; offset: number }
interface DetailResult { collection: Collection; ledgerEntries: LedgerEntry[] | null }

/** S-2 — Transaction search, with a detail view showing the collection's ledger entries. */
export function TransactionSearch({ token }: { token: string }) {
  const [ref, setRef] = useState('')
  const [status, setStatus] = useState('')
  const [applied, setApplied] = useState({ ref: '', status: '' })
  const [selected, setSelected] = useState<string | null>(null)

  const qs = new URLSearchParams({ limit: '25' })
  if (applied.ref) qs.set('merchantReference', applied.ref)
  if (applied.status) qs.set('status', applied.status)

  const list = useResource<SearchResult>(`/v1/collections?${qs}`, token)

  if (selected) return <CollectionDetail id={selected} token={token} onBack={() => setSelected(null)} />

  return (
    <div className="card">
      <h2>Transaction search</h2>
      <p className="sub">Search your collections by merchant reference or status.</p>

      <div className="row">
        <input placeholder="Merchant reference" value={ref} onChange={(e) => setRef(e.target.value)} />
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">Any status</option>
          <option value="pending">pending</option>
          <option value="cleared">cleared</option>
          <option value="settled">settled</option>
          <option value="failed">failed</option>
        </select>
        <button onClick={() => setApplied({ ref, status })}
                style={{ background: 'var(--accent)', border: 0, color: '#fff', padding: '7px 14px', borderRadius: 6, cursor: 'pointer' }}>
          Search
        </button>
      </div>

      <Resource
        what="collections" data={list.data} error={list.error} loading={list.loading} onRetry={list.reload}
        isEmpty={(d) => d.items.length === 0}
        emptyTitle="No collections match this search"
        emptyDetail="Try clearing the filters, or widen the date range."
      >
        {(d) => (
          <>
            <table>
              <thead>
                <tr>
                  <th>Reference</th><th>Channel</th><th className="num">Amount</th>
                  <th>Status</th><th>Created</th>
                </tr>
              </thead>
              <tbody>
                {d.items.map((c) => (
                  <tr key={c.id} className="clickable" onClick={() => setSelected(c.id)}>
                    <td className="mono">{c.merchantReference}</td>
                    <td className="mono">{c.channel}</td>
                    <td className="num">{money(c.amountMinor, c.currency)}</td>
                    <td><Pill status={c.status} /></td>
                    <td>{when(c.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="muted" style={{ fontSize: 12, marginTop: 10 }}>
              Showing {d.items.length} of {d.total.toLocaleString('en-US')}. Select a row for its ledger entries.
            </p>
          </>
        )}
      </Resource>
    </div>
  )
}

function CollectionDetail({ id, token, onBack }: { id: string; token: string; onBack: () => void }) {
  const { data, error, loading, reload } = useResource<DetailResult>(`/v1/collections/${id}`, token)

  return (
    <div className="card">
      <button className="back" onClick={onBack}>&larr; Back to search</button>
      <h2 style={{ marginTop: 10 }}>Collection detail</h2>
      <p className="sub mono">{id}</p>

      <Resource what="this collection" data={data} error={error} loading={loading} onRetry={reload}>
        {(d) => (
          <>
            <div className="grid">
              <div className="stat">
                <div className="label">Amount</div>
                <div className="value small">{money(d.collection.amountMinor, d.collection.currency)}</div>
              </div>
              <div className="stat">
                <div className="label">Status</div>
                <div className="value small"><Pill status={d.collection.status} /></div>
              </div>
              <div className="stat">
                <div className="label">Channel</div>
                <div className="value small mono">{d.collection.channel}</div>
              </div>
              <div className="stat">
                <div className="label">Origin</div>
                <div className="value small">{d.collection.originCountry}</div>
              </div>
            </div>

            <table style={{ marginTop: 18 }}>
              <tbody>
                <tr><th>Merchant reference</th><td className="mono">{d.collection.merchantReference}</td></tr>
                <tr><th>Customer reference</th><td className="mono">{d.collection.customerReference || '—'}</td></tr>
                <tr><th>Created</th><td>{when(d.collection.createdAt)}</td></tr>
                <tr><th>Cleared</th><td>{when(d.collection.clearedAt)}</td></tr>
                <tr><th>Settled</th><td>{when(d.collection.settledAt)}</td></tr>
                <tr><th>Settlement line</th><td className="mono">{d.collection.settlementLineId || '—'}</td></tr>
              </tbody>
            </table>

            <h2 style={{ marginTop: 22 }}>Ledger entries</h2>
            <p className="sub">
              Double-entry record for this collection. Entries sharing a transaction group sum to zero.
            </p>
            {d.ledgerEntries && d.ledgerEntries.length > 0 ? (
              <table>
                <thead>
                  <tr>
                    <th>Account</th><th>Direction</th><th className="num">Amount</th>
                    <th>Group</th><th>Created</th>
                  </tr>
                </thead>
                <tbody>
                  {d.ledgerEntries.map((e) => (
                    <tr key={e.id}>
                      <td className="mono">{e.merchantId}</td>
                      <td>{e.direction}</td>
                      <td className="num">{money(e.amountMinor, e.currency)}</td>
                      <td className="mono">{e.transactionGroupId}</td>
                      <td>{when(e.createdAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <div className="state">
                <div className="title">No ledger entries</div>
                <div className="detail">
                  A collection produces ledger entries when the channel clears it. This one is
                  still <b>{d.collection.status}</b>.
                </div>
              </div>
            )}
          </>
        )}
      </Resource>
    </div>
  )
}
