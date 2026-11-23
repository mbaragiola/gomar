import { describe, expect, it } from 'vitest'
import { draft, isPaymentMethod, matchesPaymentMethod, normalizedProducts, paymentLabels, quoteCart, saleRequest, type Grant, type PaymentMethod } from '../src/pos/model'

describe('caja sin conexión', () => {
  const grant: Grant = { actor: 8, entity: 9, outlet: 10, lease: 'turno', label: 'Centro', expires: 1000, catalog: [
    { id: 1, nombre: 'Café', precio_centavos: 1795, revision: 2, activo: true },
    { id: 2, nombre: 'Tostado', precio_centavos: 3000, revision: 1, activo: false },
    { id: 3, nombre: 'Jugo', precio_centavos: 2509, revision: 3, activo: true },
  ] }

  it('conserva los centavos, la revisión, el orden y el identificador de reintento', () => {
    const sale = draft(grant, new Map([[3, 1], [1, 2]]), 'id-fijo', 10, 'qr_modo')
    expect(sale).toMatchObject({ total: 6099, status: 'pending', key: '8:9:10' })
    expect(saleRequest(sale)).toEqual({ id_solicitud: 'id-fijo', actor_id: 8, metodo_pago: 'qr_modo', items: [
      { producto_id: 1, revision: 2, cantidad: 2 },
      { producto_id: 3, revision: 3, cantidad: 1 },
    ] })
  })

  it('excluye productos inactivos y no acepta precios mal formados', () => {
    expect(normalizedProducts(grant.catalog).map((p) => p.id)).toEqual([1, 3])
    expect(() => normalizedProducts([...grant.catalog, grant.catalog[0]])).toThrow()
    expect(() => normalizedProducts([{ ...grant.catalog[0], precio_centavos: 1.2 }])).toThrow()
    expect(() => draft(grant, new Map([[2, 1]]), 'id', 10, 'efectivo')).toThrow()
  })

  it('rechaza un turno vencido y cantidades fuera de límite', () => {
    expect(() => draft(grant, new Map([[1, 1]]), 'id', 1000, 'efectivo')).toThrow()
    expect(() => draft(grant, new Map([[1, 1000]]), 'id', 10, 'efectivo')).toThrow()
    expect(() => draft(grant, new Map([[1, 0]]), 'id', 10, 'efectivo')).toThrow()
  })

  it('cotiza la cuenta con los mismos centavos de la venta, sin registrar un pago', () => {
    const basket = new Map([[3, 1], [1, 2]])
    const quote = quoteCart(grant, basket, 10)
    const sale = draft(grant, basket, 'venta-distinta', 10, 'debito')
    expect(quote).toEqual({ lines: sale.lines, total: 6099 })
    expect(Object.keys(quote).sort()).toEqual(['lines', 'total'])
    expect([...basket]).toEqual([[3, 1], [1, 2]])
  })

  it('rechaza una cuenta vacía, vencida, de otro catálogo o con un total fuera del límite', () => {
    expect(() => quoteCart(grant, new Map(), 10)).toThrow()
    expect(() => quoteCart(grant, new Map([[1, 1]]), 1000)).toThrow()
    expect(() => quoteCart(grant, new Map([[999, 1]]), 10)).toThrow()
    const costly = { ...grant, catalog: [{ ...grant.catalog[0], precio_centavos: 100_000_000 }] }
    expect(() => quoteCart(costly, new Map([[1, 101]]), 10)).toThrow()
  })

  it('requiere un medio de pago válido y comprueba la respuesta del servidor', () => {
    expect(Object.values(paymentLabels)).toEqual([
      'Tarjeta de débito', 'Tarjeta de crédito', 'QR MODO', 'QR Mercado Pago', 'Transferencia bancaria', 'Efectivo',
    ])
    expect(isPaymentMethod('')).toBe(false)
    expect(isPaymentMethod('valueOf')).toBe(false)
    expect(() => draft(grant, new Map([[1, 1]]), 'id', 10, '' as PaymentMethod)).toThrow()
    const sale = draft(grant, new Map([[1, 1]]), 'id', 10, 'transferencia')
    expect(matchesPaymentMethod(sale, 'transferencia')).toBe(true)
    expect(matchesPaymentMethod(sale, 'efectivo')).toBe(false)
    expect(matchesPaymentMethod(sale, undefined)).toBe(false)
  })

  it('mantiene una venta anterior en efectivo y conserva su solicitud original al reintentar', () => {
    const legacy = draft(grant, new Map([[1, 1]]), 'id-anterior', 10, 'efectivo')
    delete legacy.paymentMethod
    expect(Object.hasOwn(saleRequest(legacy), 'metodo_pago')).toBe(false)
    expect(matchesPaymentMethod(legacy, 'efectivo')).toBe(true)
    expect(matchesPaymentMethod(legacy, undefined)).toBe(true)
    expect(matchesPaymentMethod(legacy, 'credito')).toBe(false)
  })

})
