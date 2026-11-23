import { describe, expect, it } from 'vitest'
import { isReconciliationCloseHandoff, parseReconciliationCloseHandoff } from '../src/reconciliation-close-handoff'

describe('reconciliation close handoff address', () => {
  it('accepts only the root, canonical numeric fragment and no query', () => {
    expect(isReconciliationCloseHandoff('/', '#conciliacion=1:2:3')).toBe(true)
    expect(isReconciliationCloseHandoff('/pos', '#conciliacion=1:2:3')).toBe(false)
    expect(parseReconciliationCloseHandoff('#conciliacion=1:2:3', '')).toEqual({ entity: 1, outlet: 2, run: 3 })
    expect(parseReconciliationCloseHandoff('#conciliacion=1:2:3', '?revision=1')).toBeNull()
  })

  it.each(['#conciliacion=01:2:3', '#conciliacion=1:0:3', '#conciliacion=1:2:9007199254740992',
    '#conciliacion=1:2:3%0A', '#%63onciliacion=1:2:3', '#conciliacion=1:2:3&revision=1',
    '#conciliacion=1:2:3\n'])('reserves malformed handoffs without resolving them: %s', (hash) => {
    expect(isReconciliationCloseHandoff('/', hash)).toBe(true)
    expect(parseReconciliationCloseHandoff(hash, '')).toBeNull()
  })
})
