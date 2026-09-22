export type Role = 'merchant' | 'finance' | 'operations'

export interface Collection {
  id: string
  merchantId: string
  channel: string
  amountMinor: number
  currency: string
  customerReference?: string
  merchantReference: string
  originCountry: string
  status: 'pending' | 'cleared' | 'failed' | 'settled'
  createdAt: string
  clearedAt?: string
  settledAt?: string
  settlementLineId?: string
}

export interface LedgerEntry {
  id: string
  merchantId: string
  direction: 'debit' | 'credit'
  amountMinor: number
  currency: string
  createdAt: string
  eventType: string
  eventId: string
  transactionGroupId: string
}

export interface SettlementRun {
  id: string
  runType: 'nightly' | 'monthEnd'
  status: 'pending' | 'running' | 'completed' | 'failed'
  startedAt: string
  endedAt?: string
  rowCount: number
  failureReason?: string
  periodStart: string
  periodEnd: string
  region: string
  netMinor?: number
  currency?: string
}

export interface ChannelAggregate {
  channel: string
  count: number
  valueMinor: number
  failedCount: number
}

export interface Aggregates {
  merchantId: string
  from: string
  to: string
  count: number
  valueMinor: number
  failedCount: number
  successRate: number
  currency: string
  byChannel: ChannelAggregate[]
  hourly: { hour: string; count: number; valueMinor: number }[]
}

export interface SettlementStatus {
  region: string
  lagSeconds: number
  oldestUnsettledAt: string | null
  lastCompletedRunAt: string | null
  lastCompletedRunId: string | null
  stalenessThresholdSeconds: number
  stale: boolean
  unsettledCount: number
  affectedMerchants: number
}

export interface Merchant {
  id: string
  name: string
  country: string
  dataRegion: string
  payoutBank: string
  payoutAccountName: string
  payoutAccountNumberMasked: string
  floatLimitMinor: number
  floatLimitCurrency: string
}

export interface MerchantView {
  merchant: Merchant
  dataRegion: string
  dataRegionStatement: string
}

export interface Balance {
  merchantId: string
  unsettledBalanceMinor: number
  floatLimitMinor: number
  headroomMinor: number
  currency: string
}

export interface Statement {
  merchantId: string
  merchantName: string
  month: string
  currency: string
  grossMinor: number
  feesMinor: number
  netMinor: number
  rowCount: number
  byChannel: { channel: string; count: number; grossMinor: number; feesMinor: number; netMinor: number }[]
  runIds: string[]
}

export interface RunHealth {
  runs: SettlementRun[]
  settlementStatus: SettlementStatus
  windowSeconds: number
  neverStarted: boolean
}

/** A merchant as merchant-api returns it (a different project, its own datastore). */
export interface MerchantSummary {
  id: string
  name: string
  country: string
  dataRegion: string
  floatLimitMinor: number
  floatLimitCurrency: string
  feeSchedules: { channel: string; percentageBp: number; fixedMinor: number }[]
}

export interface MerchantPage {
  items: MerchantSummary[]
  total: number
}
