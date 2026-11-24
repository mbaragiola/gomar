import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'

const centro = { id: 10, nombre: 'Centro' }
const norte = { id: 11, nombre: 'Norte' }
const supplier = { id: 20, razon_social: 'Proveedor de prueba', cuit: '00000000020' }
const draft = {
  id: 55, outlet: centro, supplier, moneda: 'ARS', total: '30.00', metodo: 'cash', metodo_nombre: 'Efectivo',
  referencia: 'BORRADOR CENTRO', estado: 'draft', estado_nombre: 'Borrador', tipo: 'payment', reversion_de: null,
  motivo_reversion: '', creado: '2026-10-10T00:00:00Z', registrado: null, tiene_comprobante: false,
  idempotency_key: 'centre-draft-key', aplicaciones: [],
}
const northDraft = { ...draft, id: 56, outlet: norte, referencia: 'BORRADOR NORTE', idempotency_key: 'north-draft-key' }
const newerCentroDraft = { ...draft, id: 57, referencia: 'OTRO BORRADOR CENTRO', idempotency_key: 'other-draft-key' }
const payable = {
  id: 91, documento_id: 1, referencia: 'Factura 00001-00000002', outlet: norte, supplier, moneda: 'ARS',
  fecha_emision: '2026-10-09', fecha_vencimiento: null, importe_original: '50.00', saldo: '50.00',
  estado: 'open', estado_nombre: 'Pendiente', vencida: false, registrada: '2026-10-10T00:00:00Z',
}

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
}

function deferredResponse() {
  let resolve!: (value: Response) => void
  const promise = new Promise<Response>((done) => { resolve = done })
  return { promise, resolve }
}

async function treasury(extra?: (url: string, method: string) => Promise<Response> | undefined) {
  const fetcher = vi.fn(async (url: string, options?: RequestInit) => {
    const method = options?.method ?? 'GET'
    const special = extra?.(url, method)
    if (special) return special
    if (method !== 'GET') throw new Error(`Unexpected write ${method} ${url}`)
    if (url.endsWith('/auth/csrf')) return json({})
    if (url.endsWith('/auth/me')) return json({ usuario: 'contable' })
    if (url.endsWith('/entities')) return json([
      { id: 1, nombre: 'Empresa', capacidades: ['read_business_data', 'record_supplier_payments'] },
    ])
    if (url.endsWith('/outlets')) return json([centro, norte])
    if (url.endsWith('/suppliers')) return json([supplier])
    if (url.endsWith('/ocr/config')) return json({ habilitado: false, version_aviso: '' })
    if (url.includes('/totals')) return json({ cantidad: 0, totales: [] })
    if (url.includes('/documents')) return json([])
    if (url.endsWith('/payables')) return json([payable])
    if (url.endsWith('/payments')) return json([draft, northDraft, newerCentroDraft])
    if (url.endsWith('/payments/55')) return json(draft)
    if (url.endsWith('/payments/56')) return json(northDraft)
    if (url.endsWith('/payments/57')) return json(newerCentroDraft)
    throw new Error(`Unexpected read ${url}`)
  })
  vi.stubGlobal('fetch', fetcher)
  const wrapper = mount(App)
  const workspace = () => wrapper.get('.payment-box')
  const button = (text: string) => wrapper.findAll('button').find((item) => item.text() === text)!
  const history = (reference: string) => wrapper.findAll('.ap-panel .subsection .ap-row')
    .find((row) => row.text().includes(reference))!
  const show = async () => {
    await button('Cuentas a pagar').trigger('click')
    await flushPromises()
  }
  const change = async (outlet: number) => {
    await wrapper.get('select[aria-label="Local"]').setValue(String(outlet))
    await show()
  }
  const open = async (reference: string) => {
    await history(reference).get('button').trigger('click')
    await flushPromises()
  }
  const file = async () => {
    const input = workspace().get('input[type="file"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [
      new File(['synthetic'], 'centro-qa.png', { type: 'image/png' }),
    ] })
    await input.trigger('change')
  }
  await flushPromises()
  await show()
  return { fetcher, workspace, wrapper, history, change, open, file }
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('local del borrador de tesorería', () => {
  it('descarta el formulario, el borrador, los importes y el archivo al cambiar de local', async () => {
    const { fetcher, workspace, wrapper, history, change, open, file } = await treasury()
    await workspace().get('input[placeholder="0,00"]').setValue('31,45')
    await workspace().get('input[maxlength="200"]').setValue('Referencia de Centro')
    await change(norte.id)
    expect(workspace().get<HTMLInputElement>('input[placeholder="0,00"]').element.value).toBe('')
    expect(workspace().get<HTMLInputElement>('input[maxlength="200"]').element.value).toBe('')
    await change(centro.id)
    await open(draft.referencia)
    expect(workspace().text()).toContain('Borrador #55')
    expect(workspace().text()).toContain('Local del pago: Centro')
    // Allocation across authorized outlets remains deliberate and is labeled.
    expect(workspace().get('input[placeholder="Importe a aplicar"]').element.closest('label')?.textContent).toContain('Norte')
    await workspace().get('input[placeholder="Importe a aplicar"]').setValue('30,00')
    await file()
    expect(workspace().text()).toContain('centro-qa.png')

    await change(norte.id)
    expect(workspace().text()).not.toMatch(/Borrador #55|Registrar pago|centro-qa\.png/)
    expect(workspace().find('input[type="file"]').exists()).toBe(false)
    expect(history(draft.referencia).text()).toContain('Seleccioná Centro para abrir')
    expect(history(draft.referencia).find('button').exists()).toBe(false)
    await open(northDraft.referencia)
    expect(workspace().text()).toContain('Borrador #56')
    expect(workspace().text()).toContain('Local del pago: Norte')
    expect(workspace().get<HTMLInputElement>('input[placeholder="Importe a aplicar"]').element.value).toBe('')
    expect(workspace().get<HTMLInputElement>('input[type="file"]').element.value).toBe('')
    await change(centro.id)
    await open(draft.referencia)
    expect(workspace().get<HTMLInputElement>('input[placeholder="Importe a aplicar"]').element.value).toBe('')
    expect(workspace().text()).not.toContain('centro-qa.png')
    expect(fetcher.mock.calls.filter(([, options]) => options?.method === 'POST')).toEqual([])
    wrapper.unmount()
  })

  it('no permite adjuntar al pago previo mientras abre otro borrador del mismo local', async () => {
    const pending = deferredResponse()
    const { fetcher, workspace, wrapper, open, file } = await treasury((url, method) => {
      if (url.endsWith('/payments/57') && method === 'GET') return pending.promise
      if (url.endsWith('/payments/57/receipt') && method === 'POST') return Promise.resolve(json({ detalle: 'ok' }))
    })
    await open(draft.referencia)
    await file()
    const oldInput = workspace().get('input[type="file"]').element
    const oldForm = workspace().get('form.upload').element
    await open(newerCentroDraft.referencia)
    expect(workspace().text()).toContain('Cargando pago…')
    expect(workspace().text()).not.toMatch(/Borrador #55|Registrar pago|centro-qa\.png|Crear borrador/)
    expect(workspace().find('input[type="file"]').exists()).toBe(false)
    oldInput.dispatchEvent(new Event('change', { bubbles: true }))
    oldForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))

    pending.resolve(json(newerCentroDraft))
    await flushPromises()
    expect(workspace().text()).toContain('Borrador #57')
    expect(workspace().text()).not.toContain('centro-qa.png')
    oldInput.dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()
    expect(workspace().get('.upload button').attributes('disabled')).toBeDefined()
    expect(fetcher.mock.calls.filter(([, options]) => options?.method === 'POST')).toEqual([])

    await file()
    oldForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(fetcher.mock.calls.filter(([, options]) => options?.method === 'POST')).toEqual([])
    await workspace().get('form.upload').trigger('submit')
    await flushPromises()
    const uploads = fetcher.mock.calls.filter(([, options]) => options?.method === 'POST')
    expect(uploads).toHaveLength(1)
    expect(uploads[0]?.[0]).toMatch(/\/payments\/57\/receipt$/)
    const sentFile = (uploads[0]?.[1]?.body as FormData).get('archivo') as File
    expect(sentFile.name).toBe('centro-qa.png')
    wrapper.unmount()
  })

  it('no restaura el borrador ni los datos previos si falla la apertura del siguiente', async () => {
    const pending = deferredResponse()
    const { fetcher, workspace, wrapper, open, file } = await treasury((url) =>
      url.endsWith('/payments/57') ? pending.promise : undefined)
    await open(draft.referencia)
    await file()
    await open(newerCentroDraft.referencia)
    pending.resolve(json({ detail: 'No se encontró el pago.' }, 404))
    await flushPromises()
    expect(wrapper.text()).toContain('No se encontró el pago.')
    expect(workspace().text()).not.toMatch(/Cargando pago|Borrador #55|Registrar pago|centro-qa\.png/)
    expect(workspace().get<HTMLInputElement>('input[placeholder="0,00"]').element.value).toBe('')
    expect(workspace().get<HTMLInputElement>('input[maxlength="200"]').element.value).toBe('')
    expect(fetcher.mock.calls.filter(([, options]) => options?.method === 'POST')).toEqual([])
    wrapper.unmount()
  })

  it('no vuelve a abrir el borrador demorado aunque el operador regrese al mismo local', async () => {
    const pending = deferredResponse()
    const { fetcher, workspace, wrapper, change, open } = await treasury((url) =>
      url.endsWith('/payments/55') ? pending.promise : undefined)
    await open(draft.referencia)
    expect(fetcher.mock.calls.some(([url]) => url.endsWith('/payments/55'))).toBe(true)
    await change(norte.id)
    await open(northDraft.referencia)
    expect(workspace().text()).toContain('Borrador #56')
    await change(centro.id)
    await open(newerCentroDraft.referencia)
    pending.resolve(json(draft))
    await flushPromises()
    expect(workspace().text()).toContain('Borrador #57')
    expect(workspace().text()).not.toContain('Borrador #55')
    wrapper.unmount()
  })

  it('no activa el borrador creado para el local anterior si el alta todavía estaba pendiente', async () => {
    const pending = deferredResponse()
    const { fetcher, workspace, wrapper, change, open } = await treasury((url, method) =>
      url.endsWith('/payments') && method === 'POST' ? pending.promise : undefined)
    await workspace().get('input[placeholder="0,00"]').setValue('30,00')
    await workspace().get('form.stack').trigger('submit')
    const create = fetcher.mock.calls.find(([url, options]) => url.endsWith('/payments') && options?.method === 'POST')
    expect(JSON.parse(create?.[1]?.body as string)).toMatchObject({ outlet_id: centro.id, total: '30,00' })
    await change(norte.id)
    await open(northDraft.referencia)
    pending.resolve(json(draft, 201))
    await flushPromises()
    expect(workspace().text()).toContain('Borrador #56')
    expect(workspace().text()).not.toContain('Borrador #55')
    wrapper.unmount()
  })

  it.each(['post', 'receipt'])('ignora un resultado tardío de %s para un borrador de otro local', async (action) => {
    const pending = deferredResponse()
    const endpoint = `/payments/55/${action}`
    const { fetcher, workspace, wrapper, change, open, file } = await treasury((url, method) =>
      url.endsWith(endpoint) && method === 'POST' ? pending.promise : undefined)
    await open(draft.referencia)
    if (action === 'post') {
      vi.spyOn(window, 'confirm').mockReturnValue(true)
      await workspace().get('input[placeholder="Importe a aplicar"]').setValue('30,00')
      await workspace().get('button.primary').trigger('click')
      expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('Centro'))
    } else {
      await file()
      await workspace().get('form.upload').trigger('submit')
    }
    expect(fetcher.mock.calls.some(([url, options]) => url.endsWith(endpoint) && options?.method === 'POST')).toBe(true)
    await change(norte.id)
    await open(northDraft.referencia)
    await workspace().get('input[placeholder="Importe a aplicar"]').setValue('19,00')
    pending.resolve(action === 'post'
      ? json({ ...draft, estado: 'posted', tiene_comprobante: true })
      : json({ detalle: 'ok' }))
    await flushPromises()
    expect(workspace().text()).toContain('Borrador #56')
    expect(workspace().text()).not.toContain('Borrador #55')
    expect(workspace().get<HTMLInputElement>('input[placeholder="Importe a aplicar"]').element.value).toBe('19,00')
    expect(workspace().get<HTMLInputElement>('input[type="file"]').element.value).toBe('')
    expect(fetcher.mock.calls.some(([url, options]) => url.includes('/payments/56/') && options?.method === 'POST')).toBe(false)
    wrapper.unmount()
  })
})
