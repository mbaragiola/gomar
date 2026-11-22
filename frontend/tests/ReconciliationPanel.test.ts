import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ReconciliationPanel from '../src/components/ReconciliationPanel.vue'

function response(value: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }))
}

const draftRun = {
  id: 7, entity_id: 1, outlet_ids: [10], state: 'draft', revision: 1, match_reference: false,
  unresolved_count: null, counts: { sources: 0, rows: 0, confirmed: 0, rejected: 0 }, sources: [],
}

const reviewRun = {
  ...draftRun,
  state: 'review',
}

const paymentRow = {
  id: 101, side: 'payments', source: 'pagos.csv', record_number: 2, outlet_id: 10,
  date: '2026-11-01', amount: '100.00', currency: 'ARS', currency_source: 'imported',
  category: 'Ventas', description: null, payment_method: null, reference: null,
  vat_rate: '21.00', vat_amount: '21.00',
}

const cashFlowRow = {
  ...paymentRow,
  id: 201, side: 'cash_flow', source: 'caja.csv',
}

async function mountReview(candidates: unknown[]) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (url.includes('/candidates?')) return response({ items: candidates, total: candidates.length })
    if (url.includes('/rows?') || url.includes('/events?')) return response({ items: [], total: 0 })
    if (url.endsWith('/entities/1/reconciliations/7')) return response(reviewRun)
    if (url.endsWith('/entities/1/reconciliations')) return response([reviewRun])
    return response({ detail: `Ruta inesperada: ${url}` }, 500)
  }))
  const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
  await flushPromises()
  await wrapper.get('.reconciliation-run').trigger('click')
  await flushPromises()
  return wrapper
}

function button(wrapper: ReturnType<typeof mount>, label: string) {
  return wrapper.findAll('button').find((item) => item.text() === label)!
}

function fieldSelect(wrapper: ReturnType<typeof mount>, label: string) {
  return wrapper.findAll('.fields label').find((item) => item.text().startsWith(label))!.get('select')
}

async function chooseFile(wrapper: ReturnType<typeof mount>, name: string) {
  const input = wrapper.get('input[type="file"]')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: [new File(['date,amount,outlet\n'], name, { type: 'text/csv' })],
  })
  await input.trigger('change')
}

describe('conciliación financiera', () => {
  it('no ofrece confirmar un grupo ambiguo y conserva el rechazo del par elegido', async () => {
    const wrapper = await mountReview([{
      group_id: 1, kind: 'ambiguous', payments: [paymentRow], cash_flow: [cashFlowRow],
      payment_count: 1, cash_flow_count: 1, decisions: [],
    }])
    const group = wrapper.get('.reconciliation-candidate')
    const radios = group.findAll('input[type="radio"]')
    await radios[0].setValue()
    await radios[1].setValue()

    expect(group.findAll('button').some((item) => item.text() === 'Confirmar par')).toBe(false)
    expect(group.get('button.secondary').attributes('disabled')).toBeUndefined()
  })

  it('conserva la confirmación de una candidata exacta y su reconocimiento de diferencias', async () => {
    const wrapper = await mountReview([{
      group_id: 2, kind: 'candidate', payments: [paymentRow], cash_flow: [cashFlowRow],
      payment_count: 1, cash_flow_count: 1, discrepancies: ['category'], decisions: [],
    }])
    const group = wrapper.get('.reconciliation-candidate')
    const confirm = group.findAll('button').find((item) => item.text() === 'Confirmar par')!

    expect(confirm.exists()).toBe(true)
    expect(confirm.attributes('disabled')).toBeDefined()
    await group.get('input[type="checkbox"]').setValue(true)
    expect(confirm.attributes('disabled')).toBeUndefined()
  })

  it('crea un alcance explícito y conserva varios archivos por lado', async () => {
    const fetchMock = vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/reconciliations') && !options?.method) return response([])
      if (url.endsWith('/reconciliations') && options?.method === 'POST') return response({
        id: 4, entity_id: 1, outlet_ids: [10, 11], state: 'draft', revision: 1, match_reference: false,
        unresolved_count: null, counts: { sources: 0, rows: 0, confirmed: 0, rejected: 0 }, sources: [],
      }, 201)
      return response({ detail: 'Ruta inesperada' }, 500)
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }], selectedOutletId: 10 } })
    await flushPromises()
    const checks = wrapper.findAll('input[type="checkbox"]')
    await checks[1].setValue(true)
    await wrapper.get('button.primary').trigger('click')
    await flushPromises()
    const createCall = fetchMock.mock.calls.find((call) => call[1]?.method === 'POST')
    expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({ outlet_ids: [10, 11] })
    expect(wrapper.text()).toContain('Conciliación #4')
    expect(wrapper.text()).toContain('Base de pagos')
    expect(wrapper.text()).toContain('Movimientos de caja')
  })

  it('mantiene abierta la nueva conciliación cuando termina una apertura anterior demorada', async () => {
    let listCalls = 0
    let resolveOldDetail!: (value: Response) => void
    const delayedOldDetail = new Promise<Response>((resolve) => { resolveOldDetail = resolve })
    const newRun = { ...draftRun, id: 8 }
    const fetchMock = vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/entities/1/reconciliations') && options?.method === 'POST') return response(newRun, 201)
      if (url.endsWith('/entities/1/reconciliations')) {
        listCalls += 1
        return response(listCalls === 1 ? [draftRun] : [newRun, draftRun])
      }
      if (url.endsWith('/entities/1/reconciliations/7')) return delayedOldDetail
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await button(wrapper, 'Crear conciliación').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Conciliación #8')
    expect(listCalls).toBe(2)
    resolveOldDetail(new Response(JSON.stringify(draftRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.text()).toContain('Conciliación #8')
    expect(wrapper.text()).not.toContain('Conciliación #7')
    await button(wrapper, '← Volver').trigger('click')
    expect(wrapper.findAll('.reconciliation-run')).toHaveLength(2)
  })

  it('mantiene activa una conciliación abierta mientras termina una creación y actualiza la lista', async () => {
    let listCalls = 0
    let resolveCreate!: (value: Response) => void
    let resolveDetail!: (value: Response) => void
    const delayedCreate = new Promise<Response>((resolve) => { resolveCreate = resolve })
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const newRun = { ...draftRun, id: 8 }
    const fetchMock = vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/entities/1/reconciliations') && options?.method === 'POST') return delayedCreate
      if (url.endsWith('/entities/1/reconciliations')) {
        listCalls += 1
        return response(listCalls === 1 ? [draftRun] : [newRun, draftRun])
      }
      if (url.endsWith('/entities/1/reconciliations/7')) return delayedDetail
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()

    await button(wrapper, 'Crear conciliación').trigger('click')
    await wrapper.get('.reconciliation-run').trigger('click')
    resolveCreate(new Response(JSON.stringify(newRun), { status: 201 }))
    resolveDetail(new Response(JSON.stringify(draftRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.text()).toContain('Conciliación #7')
    expect(wrapper.text()).not.toContain('Conciliación #8')
    await chooseFile(wrapper, 'existente.csv')
    expect(button(wrapper, 'Inspeccionar').attributes('disabled')).toBeUndefined()

    await button(wrapper, '← Volver').trigger('click')
    expect(wrapper.findAll('.reconciliation-run')).toHaveLength(2)
    expect(wrapper.text()).toContain('#8 · draft')
  })

  it('reinicia páginas, totales y datos de filas y eventos al cambiar de conciliación', async () => {
    const otherRun = { ...reviewRun, id: 8 }
    const requestedUrls: string[] = []
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      requestedUrls.push(url)
      if (url.endsWith('/entities/1/reconciliations')) return response([reviewRun, otherRun])
      if (url.endsWith('/entities/1/reconciliations/7')) return response(reviewRun)
      if (url.endsWith('/entities/1/reconciliations/8')) return response(otherRun)
      if (url.includes('/candidates?')) return response({ items: [], total: 0 })
      if (url.includes('/reconciliations/7/rows?page=1')) return response({ items: [{ ...paymentRow, source: 'primera.csv' }], total: 30 })
      if (url.includes('/reconciliations/7/rows?page=2')) return response({ items: [{ ...paymentRow, id: 102, source: 'segunda.csv' }], total: 30 })
      if (url.includes('/reconciliations/7/events?page=1')) return response({ items: [{ action: 'created', occurred_at: '2026-11-01T10:00:00Z', run_revision: 1 }], total: 30 })
      if (url.includes('/reconciliations/7/events?page=2')) return response({ items: [{ action: 'prepared', occurred_at: '2026-11-02T10:00:00Z', run_revision: 2 }], total: 30 })
      if (url.includes('/reconciliations/8/rows?page=1')) return response({ items: [{ ...paymentRow, id: 103, source: 'otra.csv' }], total: 1 })
      if (url.includes('/reconciliations/8/events?page=1')) return response({ items: [{ action: 'imported', occurred_at: '2026-11-03T10:00:00Z', run_revision: 3 }], total: 1 })
      if (url.includes('/rows?') || url.includes('/events?')) return response({ items: [], total: 0 })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()

    await wrapper.findAll('.reconciliation-run')[0].trigger('click')
    await flushPromises()
    await button(wrapper, 'Filas siguientes').trigger('click')
    await button(wrapper, 'Eventos siguientes').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('segunda.csv')
    expect(wrapper.text()).toContain('prepared')

    await button(wrapper, '← Volver').trigger('click')
    await wrapper.findAll('.reconciliation-run')[1].trigger('click')
    await flushPromises()

    expect(requestedUrls).toContain('/api/v1/entities/1/reconciliations/8/rows?page=1&page_size=25')
    expect(requestedUrls).toContain('/api/v1/entities/1/reconciliations/8/events?page=1&page_size=25')
    expect(wrapper.text()).toContain('otra.csv')
    expect(wrapper.text()).toContain('imported')
    expect(wrapper.text()).toContain('1 filas')
    expect(wrapper.text()).toContain('1 eventos')
    expect(wrapper.text()).not.toContain('segunda.csv')
    expect(wrapper.text()).not.toContain('prepared')
  })

  it('descarta el archivo y los resultados al cambiar de entidad', async () => {
    vi.stubGlobal('fetch', vi.fn(() => response([])))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.setProps({ entityId: 2, outlets: [{ id: 20, nombre: 'Sur' }], selectedOutletId: 20 })
    await flushPromises()
    expect(wrapper.text()).toContain('Sur')
    expect(wrapper.text()).not.toContain('Centro')
  })

  it('no muestra valores de una respuesta demorada después de cambiar de contexto', async () => {
    let resolveFirst!: (value: Response) => void
    const first = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const fetchMock = vi.fn((url: string) => url.includes('/entities/1/') ? first : response([]))
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await wrapper.setProps({ entityId: 2, outlets: [{ id: 20, nombre: 'Sur' }], selectedOutletId: 20 })
    resolveFirst(new Response(JSON.stringify([{ id: 99, counts: { rows: 99 }, state: 'draft' }]), { status: 200 }))
    await flushPromises()
    expect(wrapper.text()).not.toContain('#99')
  })

  it('descarta una inspección demorada y su File al cambiar de entidad', async () => {
    let resolveInspect!: (value: Response) => void
    const inspection = new Promise<Response>((resolve) => { resolveInspect = resolve })
    const run = {
      id: 7, entity_id: 1, outlet_ids: [10], state: 'draft', revision: 1, match_reference: false,
      unresolved_count: null, counts: { sources: 0, rows: 0, confirmed: 0, rejected: 0 }, sources: [],
    }
    vi.stubGlobal('fetch', vi.fn((url: string, options?: RequestInit) => {
      if (url.includes('/entities/1/reconciliations/7/inspect')) return inspection
      if (url.endsWith('/entities/1/reconciliations/7')) return response(run)
      if (url.endsWith('/entities/1/reconciliations')) return response([run])
      if (url.endsWith('/entities/2/reconciliations')) return response([])
      return response({ detail: `${url}:${options?.method}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['a,b\n'], 'private.csv', { type: 'text/csv' })] })
    await input.trigger('change')
    await wrapper.get('button.secondary').trigger('click')
    await wrapper.setProps({ entityId: 2, outlets: [{ id: 20, nombre: 'Sur' }], selectedOutletId: 20 })
    resolveInspect(new Response(JSON.stringify({ columnas: [{ indice: 0, encabezado: 'PRIVATE' }] }), { status: 200 }))
    await flushPromises()
    expect(wrapper.text()).not.toContain('PRIVATE')
    expect(wrapper.text()).not.toContain('private.csv')
  })

  it('libera busy al volver y descarta una inspección demorada', async () => {
    let resolveInspect!: (value: Response) => void
    const inspection = new Promise<Response>((resolve) => { resolveInspect = resolve })
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([draftRun])
      if (url.endsWith('/entities/1/reconciliations/7')) return response(draftRun)
      if (url.endsWith('/entities/1/reconciliations/7/inspect')) return inspection
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'demorado.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await button(wrapper, '← Volver').trigger('click')

    expect(wrapper.find('.reconciliation-home').exists()).toBe(true)
    expect(button(wrapper, 'Crear conciliación').attributes('disabled')).toBeUndefined()

    resolveInspect(new Response(JSON.stringify({ columnas: [{ indice: 0, encabezado: 'STALE' }] }), { status: 200 }))
    await flushPromises()

    expect(wrapper.find('.reconciliation-home').exists()).toBe(true)
    expect(wrapper.find('.reconciliation-workspace').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('STALE')
    expect(button(wrapper, 'Crear conciliación').attributes('disabled')).toBeUndefined()
  })

  it('reinicia mapeos y resoluciones y descarta la inspección anterior al reemplazar el archivo', async () => {
    let inspectCalls = 0
    let resolveInspect!: (value: Response) => void
    const delayedInspection = new Promise<Response>((resolve) => { resolveInspect = resolve })
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([draftRun])
      if (url.endsWith('/entities/1/reconciliations/7')) return response(draftRun)
      if (url.includes('/inspect')) {
        inspectCalls += 1
        if (inspectCalls === 1) return response({ columnas: [
          { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' }, { indice: 2, encabezado: 'OUTLET' },
        ] })
        return delayedInspection
      }
      if (url.includes('/preview')) return response({ rows: [], outlet_labels: ['Sucursal privada'] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'primero.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await fieldSelect(wrapper, 'Local').setValue('2')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Resolver “Sucursal privada”').setValue('10')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await chooseFile(wrapper, 'segundo.csv')
    resolveInspect(new Response(JSON.stringify({ columnas: [{ indice: 0, encabezado: 'STALE' }] }), { status: 200 }))
    await flushPromises()
    expect(wrapper.text()).toContain('segundo.csv')
    expect(wrapper.text()).not.toContain('STALE')
    expect(wrapper.text()).not.toContain('Resolver “Sucursal privada”')
    expect(wrapper.find('.fields').exists()).toBe(false)
  })

  it('conserva el archivo reemplazante cuando termina una importación anterior', async () => {
    let resolveImport!: (value: Response) => void
    const delayedImport = new Promise<Response>((resolve) => { resolveImport = resolve })
    const multiOutletDraft = { ...draftRun, outlet_ids: [10, 11] }
    const refreshedRun = { ...multiOutletDraft, revision: 2 }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([multiOutletDraft])
      if (url.endsWith('/entities/1/reconciliations/7/sources')) return delayedImport
      if (url.endsWith('/entities/1/reconciliations/7')) return response(refreshedRun)
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) return response({ rows: [{
        record_number: 2, outlet_id: 10, date: '2026-11-01', amount: '10.00', currency: 'ARS',
      }], outlet_labels: [] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'primero.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await fieldSelect(wrapper, 'Local si no está mapeado').setValue('11')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await button(wrapper, 'Importar').trigger('click')

    await chooseFile(wrapper, 'segundo.csv')
    resolveImport(new Response(JSON.stringify({ revision: 2 }), { status: 201 }))
    await flushPromises()

    expect(wrapper.text()).toContain('segundo.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    expect(fieldSelect(wrapper, 'Local si no está mapeado').element.value).toBe('11')
  })

  it('conserva el local y la vista previa reemplazantes mientras actualiza el detalle', async () => {
    let resolveImport!: (value: Response) => void
    let resolveDetail!: (value: Response) => void
    let detailCalls = 0
    const delayedImport = new Promise<Response>((resolve) => { resolveImport = resolve })
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const multiOutletDraft = { ...draftRun, outlet_ids: [10, 11] }
    const refreshedRun = { ...multiOutletDraft, revision: 2 }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([multiOutletDraft])
      if (url.endsWith('/entities/1/reconciliations/7/sources')) return delayedImport
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(multiOutletDraft) : delayedDetail
      }
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) return response({ rows: [{
        record_number: 2, outlet_id: 11, date: '2026-11-01', amount: '10.00', currency: 'ARS',
      }], outlet_labels: [] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'primero.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await button(wrapper, 'Importar').trigger('click')

    await chooseFile(wrapper, 'segundo.csv')
    resolveImport(new Response(JSON.stringify({ revision: 2 }), { status: 201 }))
    await flushPromises()
    expect(detailCalls).toBe(2)

    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await fieldSelect(wrapper, 'Local si no está mapeado').setValue('11')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()

    resolveDetail(new Response(JSON.stringify(refreshedRun), { status: 200 }))
    await flushPromises()

    expect(fieldSelect(wrapper, 'Local si no está mapeado').element.value).toBe('11')
    expect(wrapper.text()).toContain('segundo.csv')
    expect(fieldSelect(wrapper, 'Fecha').element.value).toBe('0')
    expect(fieldSelect(wrapper, 'Importe').element.value).toBe('1')
    expect(wrapper.get('.reconciliation-table').text()).toContain('Norte')
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()
  })

  it('conserva el reemplazo elegido después de iniciar la actualización del detalle', async () => {
    let resolveDetail!: (value: Response) => void
    let detailCalls = 0
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const multiOutletDraft = { ...draftRun, outlet_ids: [10, 11] }
    const refreshedRun = { ...multiOutletDraft, revision: 2 }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([multiOutletDraft])
      if (url.endsWith('/entities/1/reconciliations/7/sources')) return response({ revision: 2 }, 201)
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(multiOutletDraft) : delayedDetail
      }
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) return response({ rows: [{
        record_number: 2, outlet_id: 11, date: '2026-11-01', amount: '10.00', currency: 'ARS',
      }], outlet_labels: [] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'primero.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await button(wrapper, 'Importar').trigger('click')
    await flushPromises()
    expect(detailCalls).toBe(2)

    await chooseFile(wrapper, 'segundo.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await fieldSelect(wrapper, 'Local si no está mapeado').setValue('11')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()

    resolveDetail(new Response(JSON.stringify(refreshedRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.text()).toContain('segundo.csv')
    expect(fieldSelect(wrapper, 'Fecha').element.value).toBe('0')
    expect(fieldSelect(wrapper, 'Importe').element.value).toBe('1')
    expect(fieldSelect(wrapper, 'Local si no está mapeado').element.value).toBe('11')
    expect(wrapper.get('.reconciliation-table').text()).toContain('Norte')
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()
  })

  it('conserva el local elegido cuando la importación responde antes de actualizar el detalle', async () => {
    let resolveImport!: (value: Response) => void
    let resolveDetail!: (value: Response) => void
    let detailCalls = 0
    const delayedImport = new Promise<Response>((resolve) => { resolveImport = resolve })
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const multiOutletDraft = { ...draftRun, outlet_ids: [10, 11] }
    const refreshedRun = { ...multiOutletDraft, revision: 2 }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([multiOutletDraft])
      if (url.endsWith('/entities/1/reconciliations/7/sources')) return delayedImport
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(multiOutletDraft) : delayedDetail
      }
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) return response({ rows: [{
        record_number: 2, outlet_id: 11, date: '2026-11-01', amount: '10.00', currency: 'ARS',
      }], outlet_labels: [] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'primero.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await button(wrapper, 'Importar').trigger('click')

    resolveImport(new Response(JSON.stringify({ revision: 2 }), { status: 201 }))
    await flushPromises()
    expect(detailCalls).toBe(2)

    await chooseFile(wrapper, 'segundo.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await fieldSelect(wrapper, 'Local si no está mapeado').setValue('11')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()

    resolveDetail(new Response(JSON.stringify(refreshedRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.text()).toContain('segundo.csv')
    expect(fieldSelect(wrapper, 'Fecha').element.value).toBe('0')
    expect(fieldSelect(wrapper, 'Importe').element.value).toBe('1')
    expect(fieldSelect(wrapper, 'Local si no está mapeado').element.value).toBe('11')
    expect(wrapper.get('.reconciliation-table').text()).toContain('Norte')
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()
  })

  it('no reabre la conciliación al volver durante la actualización posterior a importar', async () => {
    let detailCalls = 0
    let resolveDetail!: (value: Response) => void
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const refreshedRun = { ...draftRun, revision: 2 }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([draftRun])
      if (url.endsWith('/entities/1/reconciliations/7/sources')) return response({ revision: 2 }, 201)
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(draftRun) : delayedDetail
      }
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) return response({ rows: [{
        record_number: 2, outlet_id: 10, date: '2026-11-01', amount: '10.00', currency: 'ARS',
      }], outlet_labels: [] })
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'importar.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    await button(wrapper, 'Importar').trigger('click')
    await flushPromises()

    expect(detailCalls).toBe(2)
    await button(wrapper, '← Volver').trigger('click')
    resolveDetail(new Response(JSON.stringify(refreshedRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.find('.reconciliation-workspace').exists()).toBe(false)
    expect(wrapper.find('.reconciliation-home').exists()).toBe(true)
  })

  it('no reabre la conciliación eliminada cuando termina una actualización demorada', async () => {
    let listCalls = 0
    let detailCalls = 0
    let resolveDetail!: (value: Response) => void
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const runWithSource = {
      ...draftRun,
      counts: { ...draftRun.counts, sources: 1 },
      sources: [{ id: 3, label: 'pagos.csv', row_count: 1 }],
    }
    vi.stubGlobal('confirm', vi.fn(() => true))
    vi.stubGlobal('fetch', vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/entities/1/reconciliations')) {
        listCalls += 1
        return response(listCalls === 1 ? [runWithSource] : [])
      }
      if (url.includes('/entities/1/reconciliations/7/sources/3?') && options?.method === 'DELETE') {
        return response({ revision: 2 })
      }
      if (url.includes('/entities/1/reconciliations/7?') && options?.method === 'DELETE') return response({})
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(runWithSource) : delayedDetail
      }
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await button(wrapper, 'Quitar').trigger('click')
    await flushPromises()
    expect(detailCalls).toBe(2)

    await button(wrapper, 'Eliminar').trigger('click')
    await flushPromises()
    resolveDetail(new Response(JSON.stringify({ ...runWithSource, revision: 2 }), { status: 200 }))
    await flushPromises()

    expect(wrapper.find('.reconciliation-workspace').exists()).toBe(false)
    expect(wrapper.find('.reconciliation-home').exists()).toBe(true)
    expect(wrapper.findAll('.reconciliation-run')).toHaveLength(0)
  })

  it('ignora el detalle anterior cuando preparar avanza la revisión durante la actualización', async () => {
    let detailCalls = 0
    let resolveDetail!: (value: Response) => void
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    const runWithThreeSources = {
      ...draftRun,
      revision: 1,
      counts: { ...draftRun.counts, sources: 3 },
      sources: [
        { id: 1, label: 'pagos.csv', row_count: 1 },
        { id: 2, label: 'caja.csv', row_count: 1 },
        { id: 3, label: 'ajustes.csv', row_count: 1 },
      ],
    }
    const detailAfterDelete = {
      ...runWithThreeSources,
      revision: 2,
      counts: { ...runWithThreeSources.counts, sources: 2 },
      sources: runWithThreeSources.sources.slice(1),
    }
    const preparedRun = { ...detailAfterDelete, state: 'review', revision: 3 }
    const fetchMock = vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([runWithThreeSources])
      if (url.includes('/entities/1/reconciliations/7/sources/1?') && options?.method === 'DELETE') {
        return response({ revision: 2 })
      }
      if (url.endsWith('/entities/1/reconciliations/7/prepare') && options?.method === 'POST') {
        return response(preparedRun)
      }
      if (url.endsWith('/entities/1/reconciliations/7/close') && options?.method === 'POST') {
        return response(preparedRun)
      }
      if (url.includes('/candidates?') || url.includes('/rows?') || url.includes('/events?')) {
        return response({ items: [], total: 0 })
      }
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(runWithThreeSources) : delayedDetail
      }
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    })
    vi.stubGlobal('confirm', vi.fn(() => true))
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()

    await wrapper.findAll('button').find((item) => item.text() === 'Quitar')!.trigger('click')
    await flushPromises()
    expect(detailCalls).toBe(2)
    await button(wrapper, 'Preparar revisión').trigger('click')
    await flushPromises()

    resolveDetail(new Response(JSON.stringify(detailAfterDelete), { status: 200 }))
    await flushPromises()

    expect(wrapper.get('.status').text()).toBe('review')
    await button(wrapper, 'Cerrar y conservar pendientes').trigger('click')
    await flushPromises()
    const closeCall = fetchMock.mock.calls.find((call) => String(call[0]).endsWith('/entities/1/reconciliations/7/close'))
    expect(JSON.parse(String(closeCall?.[1]?.body))).toMatchObject({ expected_revision: 3 })
  })

  it('no reabre una conciliación eliminada al volver a abrirla durante la eliminación', async () => {
    let listCalls = 0
    let detailCalls = 0
    let resolveDelete!: (value: Response) => void
    let resolveDetail!: (value: Response) => void
    const delayedDelete = new Promise<Response>((resolve) => { resolveDelete = resolve })
    const delayedDetail = new Promise<Response>((resolve) => { resolveDetail = resolve })
    vi.stubGlobal('confirm', vi.fn(() => true))
    vi.stubGlobal('fetch', vi.fn((url: string, options?: RequestInit) => {
      if (url.endsWith('/entities/1/reconciliations')) {
        listCalls += 1
        return response(listCalls === 1 ? [draftRun] : [])
      }
      if (url.includes('/entities/1/reconciliations/7?') && options?.method === 'DELETE') return delayedDelete
      if (url.endsWith('/entities/1/reconciliations/7')) {
        detailCalls += 1
        return detailCalls === 1 ? response(draftRun) : delayedDetail
      }
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await button(wrapper, 'Eliminar').trigger('click')
    await button(wrapper, '← Volver').trigger('click')
    await wrapper.get('.reconciliation-run').trigger('click')

    resolveDelete(new Response(JSON.stringify({}), { status: 200 }))
    await flushPromises()
    resolveDetail(new Response(JSON.stringify(draftRun), { status: 200 }))
    await flushPromises()

    expect(wrapper.find('.reconciliation-workspace').exists()).toBe(false)
    expect(wrapper.find('.reconciliation-home').exists()).toBe(true)
    expect(wrapper.findAll('.reconciliation-run')).toHaveLength(0)
  })

  it('invalida la vista previa al cambiar configuración y descarta una respuesta demorada', async () => {
    let previewCalls = 0
    let resolvePreview!: (value: Response) => void
    const delayedPreview = new Promise<Response>((resolve) => { resolvePreview = resolve })
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/entities/1/reconciliations')) return response([draftRun])
      if (url.endsWith('/entities/1/reconciliations/7')) return response(draftRun)
      if (url.includes('/inspect')) return response({ columnas: [
        { indice: 0, encabezado: 'DATE' }, { indice: 1, encabezado: 'AMOUNT' },
      ] })
      if (url.includes('/preview')) {
        previewCalls += 1
        if (previewCalls === 1) return response({ rows: [{
          record_number: 2, outlet_id: 10, date: '2026-11-01', amount: '10.00', currency: 'ARS',
        }], outlet_labels: [] })
        return delayedPreview
      }
      return response({ detail: `Ruta inesperada: ${url}` }, 500)
    }))
    const wrapper = mount(ReconciliationPanel, { props: { entityId: 1, outlets: [{ id: 10, nombre: 'Centro' }], selectedOutletId: 10 } })
    await flushPromises()
    await wrapper.get('.reconciliation-run').trigger('click')
    await flushPromises()
    await chooseFile(wrapper, 'config.csv')
    await button(wrapper, 'Inspeccionar').trigger('click')
    await flushPromises()
    await fieldSelect(wrapper, 'Fecha').setValue('0')
    await fieldSelect(wrapper, 'Importe').setValue('1')
    await button(wrapper, 'Vista previa').trigger('click')
    await flushPromises()
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('10.00')

    await fieldSelect(wrapper, 'Formato de fecha').setValue('%d/%m/%Y')
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).not.toContain('10.00')
    await button(wrapper, 'Vista previa').trigger('click')
    await fieldSelect(wrapper, 'Separador decimal').setValue(',')
    resolvePreview(new Response(JSON.stringify({ rows: [{
      record_number: 2, outlet_id: 10, date: '2026-11-01', amount: '999.00', currency: 'ARS',
    }], outlet_labels: [] }), { status: 200 }))
    await flushPromises()
    expect(wrapper.text()).not.toContain('999.00')
    expect(button(wrapper, 'Importar').attributes('disabled')).toBeDefined()
  })
})
