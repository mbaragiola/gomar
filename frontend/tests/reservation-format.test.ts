import { describe, expect, it } from 'vitest'
import { reservationSlotUTC, reservationTimes } from '../src/reservation-format'

describe('turnos de Buenos Aires', () => {
  it('genera tramos de 90 minutos y conserva la fecha local al cruzar medianoche UTC', () => {
    expect(reservationTimes).toHaveLength(16)
    expect(reservationTimes.at(-1)).toBe('22:30')
    expect(reservationSlotUTC('2030-04-08', '22:30')).toBe('2030-04-09T01:30:00Z')
  })

  it('rechaza fechas inexistentes y horas fuera de tramo', () => {
    expect(reservationSlotUTC('2030-02-30', '18:00')).toBe('')
    expect(reservationSlotUTC('2030-04-08', '19:00')).toBe('')
  })
})
