import { todayInBuenosAires } from '../format'
import { sameContext, type Context, type Sale } from './model'
import { validUTC, type ReceiptClock } from './sale-time'

export interface OutstandingSale {
  id: string
  status: 'pending' | 'attention'
  tillReportedAt?: string
}

export interface CrossMonthSale {
  id: string
  clock: ReceiptClock
}

const monthOf = (instant: Date) => todayInBuenosAires(instant).slice(0, -3)

export function precloseSummary(scope: Context, sales: readonly Sale[], checkedAt: number) {
  const current = monthOf(new Date(checkedAt))
  const [year, month] = current.split('-').map(Number)
  const previous = month === 1 ? `${year - 1}-12` : `${year}-${String(month - 1).padStart(2, '0')}`
  const outstanding: OutstandingSale[] = []
  const crossMonth: CrossMonthSale[] = []
  let pending = 0
  let attention = 0
  let unknownRegistration = 0
  let unknownTill = 0

  for (const sale of sales) {
    if (!sameContext(scope, sale)) continue
    if (sale.status === 'pending' || sale.status === 'attention') {
      const tillReportedAt = validUTC(sale.tillReportedAt) ? sale.tillReportedAt : undefined
      outstanding.push({ id: sale.id, status: sale.status, tillReportedAt })
      if (sale.status === 'pending') pending++
      else attention++
    } else if (sale.status === 'synced') {
      // An acknowledged, immutable pair from Gomar is the only evidence for
      // the comparison. Never infer it from enqueue time or a retried claim.
      const clock = sale.receiptClock
      if (!validUTC(clock?.registeredAt)) {
        unknownRegistration++
        continue
      }
      const registrationMonth = monthOf(new Date(clock.registeredAt))
      if (registrationMonth !== current && registrationMonth !== previous) continue
      if (!validUTC(clock.tillReportedAt) || (clock.status !== 'unverified' && clock.status !== 'review')) {
        unknownTill++
        continue
      }
      if (monthOf(new Date(clock.tillReportedAt)) !== registrationMonth) crossMonth.push({ id: sale.id, clock })
    }
  }

  return { current, previous, pending, attention, outstanding, unknownRegistration, unknownTill, crossMonth }
}
