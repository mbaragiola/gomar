import { describe, expect, it } from 'vitest'
import { type Sale } from '../src/pos/model'
import { compareSaleReceipt } from '../src/pos/sale-review'

const sale: Sale = {
  id: '92000000-0000-4000-8000-000000000001', actor: 3, entity: 1, outlet: 2, key: '3:1:2',
  created: Date.parse('2026-11-27T16:00:00Z'), total: 1900, status: 'attention',
  lines: [
    { producto_id: 4, revision: 2, cantidad: 1, nombre: 'Pan', precio_centavos: 700 },
    { producto_id: 9, revision: 3, cantidad: 2, nombre: 'Café', precio_centavos: 600 },
  ],
}
const items = [
  { producto_id: 9, revision: 3, cantidad: 2 },
  { producto_id: 4, revision: 2, cantidad: 1 },
]
const receipt = {
  id: 180, id_solicitud: sale.id, actor_id: 3, metodo_pago: 'efectivo', total_centavos: 1900,
  creado_en: '2026-11-27T16:05:00Z', estado_hora_caja: 'not_reported', items,
}

describe('scoped POS recovery evidence', () => {
  it('matches immutable receipt fields regardless of item order and preserves its original clock', () => {
    expect(compareSaleReceipt(sale, receipt)).toEqual({
      state: 'match', serverId: 180, clock: { registeredAt: receipt.creado_en, status: 'not_reported' },
    })
  })

  it.each([
    { total_centavos: 1901 }, { metodo_pago: 'debito' }, { items: [items[0]] },
    { items: [{ ...items[0], cantidad: 1 }, items[1]] }, { items: [{ ...items[0], revision: 4 }, items[1]] },
    { items: [{ ...items[0], producto_id: 5 }, items[1]] },
  ])('preserves a conflicting sale for review: %j', (difference) => {
    expect(compareSaleReceipt(sale, { ...receipt, ...difference })).toEqual({ state: 'conflict' })
  })

  it.each([
    { actor_id: 4 }, { id_solicitud: 'other' }, { id: 0 }, { creado_en: 'yesterday' },
    { metodo_pago: 'invalido' }, { items: [items[0], items[0]] }, { items: [] },
  ])('keeps malformed or foreign evidence unresolved: %j', (difference) => {
    expect(compareSaleReceipt(sale, { ...receipt, ...difference })).toEqual({ state: 'unresolved' })
  })
})
