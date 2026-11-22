import { describe, expect, it } from 'vitest'
import { localDate, localDateTime, money, todayInBuenosAires } from '../src/format'

describe('formatos es-AR', () => {
  it('formatea dinero en la moneda del comprobante', () => {
    expect(money('99999999999999.01', 'ARS')).toBe('$ 99.999.999.999.999,01')
    expect(money('-99999999999999.01', 'USD')).toBe('-US$ 99.999.999.999.999,01')
    expect(money('21.1', 'USD')).toBe('US$ 21,10')
  })

  it('interpreta fechas de negocio como fechas locales', () => {
    expect(localDate('2026-10-09')).toMatch(/9\/10\/2026/)
    expect(todayInBuenosAires(new Date('2026-10-09T01:30:00Z'))).toBe('2026-10-08')
    expect(localDateTime('2026-10-09T03:30:00Z')).toMatch(/00:30/)
  })
})
