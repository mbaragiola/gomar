import { isPaymentMethod, matchesPaymentMethod, maxLines, maxQuantity, maxTotal, positive, type Sale } from './model'
import { receiptClock } from './sale-time'

export function compareSaleReceipt(sale: Sale, payload: unknown) {
  if (!payload || typeof payload !== 'object') return { state: 'unresolved' } as const
  const receipt = payload as Record<string, unknown>
  const clock = receiptClock(receipt)
  if (receipt.id_solicitud !== sale.id || receipt.actor_id !== sale.actor ||
    typeof receipt.id !== 'number' || !positive(receipt.id) ||
    typeof receipt.total_centavos !== 'number' || !positive(receipt.total_centavos) || receipt.total_centavos > maxTotal ||
    !isPaymentMethod(receipt.metodo_pago) || !clock || !Array.isArray(receipt.items) ||
    !receipt.items.length || receipt.items.length > maxLines) return { state: 'unresolved' } as const
  const lines = new Map<number, { revision: number; quantity: number }>()
  for (const item of receipt.items) {
    if (!item || !positive(item.producto_id) || !positive(item.revision) || !positive(item.cantidad) ||
      item.cantidad > maxQuantity || lines.has(item.producto_id)) return { state: 'unresolved' } as const
    lines.set(item.producto_id, { revision: item.revision, quantity: item.cantidad })
  }
  if (receipt.total_centavos !== sale.total || !matchesPaymentMethod(sale, receipt.metodo_pago) ||
    lines.size !== sale.lines.length || sale.lines.some((line) => {
      const saved = lines.get(line.producto_id)
      return saved?.revision !== line.revision || saved.quantity !== line.cantidad
    })) return { state: 'conflict' } as const
  return { state: 'match', serverId: receipt.id, clock } as const
}
