import { describe, expect, it } from 'vitest'
import { isPayableEligibilityHandoff, parsePayableEligibilityHandoff, payableEligibilityReference } from '../src/payable-eligibility-handoff'

describe('payable eligibility browser handoff', () => {
  const sample = '#alta-cuenta=1:2:3'
  const target = { entity: 1, outlet: 2, document: 3 }
  it('reserves only a root-path handoff and strictly parses canonical safe integers', () => {
    expect(isPayableEligibilityHandoff('/', sample)).toBe(true)
    expect(isPayableEligibilityHandoff('/pos', sample)).toBe(false)
    expect(parsePayableEligibilityHandoff(sample, '')).toEqual(target)
    expect(parsePayableEligibilityHandoff(sample, '?')).toBeNull()
    expect(parsePayableEligibilityHandoff(sample, '?x=1')).toBeNull()
    for (const hash of ['#alta-cuenta=01:2:3', '#alta-cuenta=1:0:3', '#alta-cuenta=1:2:9007199254740992',
      '#alta-cuenta=1:2:3%0A', '#%61lta-cuenta=1:2:3%XX', '#alta-cuenta=1:2:3&x=4', '#ALTA-CUENTA=1:2:3']) {
      expect(isPayableEligibilityHandoff('/', hash)).toBe(true)
      expect(parsePayableEligibilityHandoff(hash, '')).toBeNull()
    }
  })

  it('accepts only the same document and safe canonical invoice reference', () => {
    const printed = { document_type: 'invoice', letter: 'A', point_of_sale: 1, number: 4 }
    const body = { document_id: 3, printed_reference: printed }
    expect(payableEligibilityReference(body, target)).toEqual(printed)
    expect(payableEligibilityReference({ ...body, document_id: 4 }, target)).toBeNull()
    expect(payableEligibilityReference({ ...body, total: 6 }, target)).toBeNull()
    expect(payableEligibilityReference({ ...body, printed_reference: { ...printed, point_of_sale: 0 } }, target)).toBeNull()
    expect(payableEligibilityReference({ ...body, printed_reference: { ...printed, letter: 'legacy/private' } }, target)).toBeNull()
  })
})
