import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'

function response(value: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  }))
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

describe('aplicación', () => {
  const administrationCapabilities = [
    'read_business_data',
    'download_attachments',
    'manage_suppliers',
    'manage_draft_documents',
    'manage_reviewed_documents',
    'review_documents',
    'manage_entity_access',
    'record_supplier_payments',
    'reverse_supplier_payments',
    'export_financial_reports',
  ]
  const document = {
    id: 1,
    entity_id: 1,
    outlet: { id: 10, nombre: 'Centro' },
    supplier: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' },
    tipo: 'invoice',
    tipo_nombre: 'Factura',
    letra: '',
    punto_venta: 1,
    numero: 2,
    referencia: 'Factura 00001-00000002',
    fecha_emision: '2026-10-09',
    fecha_vencimiento: null,
    moneda: 'ARS',
    neto: '100.10',
    iva: '21.02',
    total: '121.12',
    descripcion: 'Compra sintética',
    estado: 'draft',
    estado_nombre: 'Pendiente',
    revision: 1,
    creado: '2026-10-09T12:00:00Z',
    actualizado: '2026-10-09T12:00:00Z',
    adjuntos: [],
    auditoria: [],
  }

  function appFetch(extra?: (url: string, options?: RequestInit) => Promise<Response> | undefined) {
    return vi.fn((url: string, options?: RequestInit) => {
      const special = extra?.(url, options)
      if (special) return special
      if (url.endsWith('/auth/csrf')) return response({ detalle: 'ok' })
      if (url.endsWith('/auth/me')) return response({ usuario: 'gestor' })
      if (url.endsWith('/entities')) return response([{ id: 1, nombre: 'Entidad Uno SA', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities }])
      if (url.endsWith('/outlets')) return response([{ id: 10, nombre: 'Centro' }])
      if (url.endsWith('/suppliers')) return response([{ id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' }])
      if (url.includes('/totals')) return response({ cantidad: 0, totales: [] })
      if (url.includes('/documents')) return response([])
      return response({ detail: 'Ruta inesperada' }, 500)
    })
  }

  it('muestra el ingreso en español cuando no hay sesión', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/auth/csrf')) return response({ detalle: 'ok' })
      return response({ detail: 'No autorizado' }, 401)
    }))
    const wrapper = mount(App)
    await flushPromises()
    expect(wrapper.text()).toContain('Registro de compras')
    expect(wrapper.text()).toContain('No hay registro público')
    expect(wrapper.find('button').text()).toBe('Ingresar')
  })

  it('muestra sólo el contexto y los totales asignados', async () => {
    vi.stubGlobal('fetch', appFetch())
    const wrapper = mount(App)
    await flushPromises()
    expect(wrapper.text()).toContain('Entidad Uno SA')
    expect(wrapper.text()).toContain('Centro')
    expect(wrapper.text()).toContain('Nuevo comprobante')
    expect(wrapper.text()).toContain('Todavía no hay comprobantes')
  })

  it.each([
    {
      role: 'read_only',
      capabilities: ['read_business_data'],
      visible: [] as string[],
      hidden: ['Nuevo comprobante', 'Gestionar proveedores', 'Gestionar accesos', 'Descargar', 'Marcar revisado', 'Dar de baja'],
    },
    {
      role: 'purchasing',
      capabilities: ['read_business_data', 'download_attachments', 'manage_suppliers', 'manage_draft_documents'],
      visible: ['Nuevo comprobante', 'Gestionar proveedores', 'Descargar', 'Dar de baja'],
      hidden: ['Gestionar accesos', 'Marcar revisado'],
    },
    {
      role: 'accounting',
      capabilities: ['read_business_data', 'download_attachments', 'review_documents'],
      visible: ['Descargar', 'Marcar revisado'],
      hidden: ['Nuevo comprobante', 'Gestionar proveedores', 'Gestionar accesos', 'Dar de baja'],
    },
  ])('muestra sólo las acciones permitidas para $role', async ({ role, capabilities, visible, hidden }) => {
    const detail = {
      ...document,
      adjuntos: [{ id: 7, nombre: 'privado.pdf', tamano: 24 }],
    }
    vi.stubGlobal('fetch', appFetch((url) => {
      if (url.endsWith('/entities')) return response([{
        id: 1,
        nombre: 'Entidad Uno SA',
        cuit: '30111111118',
        rol: role,
        rol_nombre: role,
        capacidades: capabilities,
      }])
      if (url.includes('/documents?')) return response([detail])
      if (url.endsWith('/documents/1')) return response(detail)
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    for (const label of visible) expect(wrapper.text()).toContain(label)
    for (const label of hidden) expect(wrapper.text()).not.toContain(label)
  })

  it('deshabilita membresías propias, de plataforma o fuera del alcance visible', async () => {
    vi.stubGlobal('fetch', appFetch((url) => {
      if (url.endsWith('/management')) return response({
        entidad: { id: 1, nombre: 'Entidad Uno SA', cuit: '30111111118' },
        roles: [
          { valor: 'unassigned', nombre: 'Sin rol' },
          { valor: 'administration', nombre: 'Administración' },
          { valor: 'read_only', nombre: 'Consulta' },
        ],
        locales: [
          { id: 10, nombre: 'Centro', activa: true, autorizado: true },
          { id: 11, nombre: 'Norte', activa: true, autorizado: false },
        ],
        membresias: [
          { id: 1, usuario: 'gestor', rol: 'administration', rol_nombre: 'Administración', activa: true, editable: false, outlet_ids: [10] },
          { id: 2, usuario: 'fuera-de-alcance', rol: 'read_only', rol_nombre: 'Consulta', activa: true, editable: false, outlet_ids: [10] },
          { id: 3, usuario: 'editable', rol: 'read_only', rol_nombre: 'Consulta', activa: true, editable: true, outlet_ids: [10] },
        ],
      })
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Gestionar accesos')?.trigger('click')
    await flushPromises()
    const cards = wrapper.findAll('.membership-card').filter((card) => card.find('strong').exists())
    const outside = cards.find((card) => card.text().includes('fuera-de-alcance'))!
    const editable = cards.find((card) => card.text().includes('editable'))!
    expect(outside.find('select').attributes('disabled')).toBeDefined()
    expect(outside.text()).toContain('locales fuera de tu alcance')
    expect(editable.find('select').attributes('disabled')).toBeUndefined()
    expect(editable.find('button').attributes('disabled')).toBeUndefined()
  })

  it('reinicia formularios de acceso y descarta callbacks demorados al cambiar de entidad', async () => {
    const delayedCreate = deferred<Response>()
    const management = (id: number) => ({
      entidad: { id, nombre: `Entidad ${id}`, cuit: id === 1 ? '30111111118' : '30222222223' },
      roles: [
        { valor: 'unassigned', nombre: 'Sin rol' },
        { valor: 'administration', nombre: 'Administración' },
      ],
      locales: [{ id: id * 10, nombre: `Local ${id}`, activa: true, autorizado: true }],
      membresias: [],
    })
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/entities')) return response([
        { id: 1, nombre: 'Entidad 1', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
        { id: 2, nombre: 'Entidad 2', cuit: '30222222223', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
      ])
      if (url.includes('/entities/1/outlets') && options?.method !== 'POST') return response([{ id: 10, nombre: 'Local 1' }])
      if (url.includes('/entities/2/outlets') && options?.method !== 'POST') return response([{ id: 20, nombre: 'Local 2' }])
      if (url.includes('/entities/1/suppliers') || url.includes('/entities/2/suppliers')) return response([])
      if (url.endsWith('/entities/1/management')) return response(management(1))
      if (url.endsWith('/entities/2/management')) return response(management(2))
      if (url.endsWith('/entities/1/outlets') && options?.method === 'POST') return delayedCreate.promise
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Gestionar accesos')?.trigger('click')
    await flushPromises()
    const outletForm = wrapper.find('.management-section form.management-row')
    const membershipForm = wrapper.find('form.membership-card')
    await outletForm.find('input:not([type="checkbox"])').setValue('Local demorado')
    await outletForm.find('input[type="checkbox"]').setValue(false)
    await membershipForm.find('input[autocomplete="off"]').setValue('usuario-viejo')
    await membershipForm.find('select').setValue('administration')
    await membershipForm.findAll('input[type="checkbox"]')[0].setValue(false)
    await membershipForm.findAll('input[type="checkbox"]')[1].setValue(true)
    await outletForm.trigger('submit')

    await wrapper.find('.context label:first-child select').setValue(2)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Gestionar accesos')?.trigger('click')
    await flushPromises()
    const currentOutletInput = wrapper.find('.management-section form.management-row input:not([type="checkbox"])')
    const currentOutletActive = wrapper.find('.management-section form.management-row input[type="checkbox"]')
    const currentMembershipInput = wrapper.find('form.membership-card input[autocomplete="off"]')
    const currentMembershipForm = wrapper.find('form.membership-card')
    expect((currentOutletInput.element as HTMLInputElement).value).toBe('')
    expect((currentOutletActive.element as HTMLInputElement).checked).toBe(true)
    expect((currentMembershipInput.element as HTMLInputElement).value).toBe('')
    expect((currentMembershipForm.find('select').element as HTMLSelectElement).value).toBe('unassigned')
    expect((currentMembershipForm.findAll('input[type="checkbox"]')[0].element as HTMLInputElement).checked).toBe(true)
    expect((currentMembershipForm.findAll('input[type="checkbox"]')[1].element as HTMLInputElement).checked).toBe(false)
    await currentOutletInput.setValue('Local seguro')
    await currentMembershipInput.setValue('usuario-seguro')

    delayedCreate.resolve(new Response(JSON.stringify({ id: 12 }), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    }))
    await flushPromises()
    expect((currentOutletInput.element as HTMLInputElement).value).toBe('Local seguro')
    expect((currentMembershipInput.element as HTMLInputElement).value).toBe('usuario-seguro')
    expect(wrapper.text()).toContain('Entidad 2')
    expect(wrapper.text()).not.toContain('Local demorado')
  })

  it('ignora una carga de accesos demorada de la entidad anterior', async () => {
    const delayedManagement = deferred<Response>()
    const management = (id: number, name: string) => ({
      entidad: { id, nombre: name, cuit: id === 1 ? '30111111118' : '30222222223' },
      roles: [{ valor: 'unassigned', nombre: 'Sin rol' }],
      locales: [],
      membresias: [],
    })
    vi.stubGlobal('fetch', appFetch((url) => {
      if (url.endsWith('/entities')) return response([
        { id: 1, nombre: 'Entidad 1', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
        { id: 2, nombre: 'Entidad 2', cuit: '30222222223', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
      ])
      if (url.endsWith('/entities/1/management')) return delayedManagement.promise
      if (url.endsWith('/entities/2/management')) return response(management(2, 'Entidad 2 vigente'))
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Gestionar accesos')?.trigger('click')
    await wrapper.find('.context label:first-child select').setValue(2)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Gestionar accesos')?.trigger('click')
    await flushPromises()
    expect((wrapper.find('.management-card input').element as HTMLInputElement).value).toBe('Entidad 2 vigente')

    delayedManagement.resolve(new Response(JSON.stringify(management(1, 'ENTIDAD PRIVADA DEMORADA')), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    await flushPromises()
    expect((wrapper.find('.management-card input').element as HTMLInputElement).value).toBe('Entidad 2 vigente')
    expect(wrapper.text()).not.toContain('ENTIDAD PRIVADA DEMORADA')
  })

  it('envía importes con coma y moneda sin convertirlos', async () => {
    let sent: Record<string, unknown> | null = null
    const fetchMock = appFetch((url, options) => {
      if (url.endsWith('/documents') && options?.method === 'POST') {
        sent = JSON.parse(String(options.body))
        return response(document, 201)
      }
      if (url.endsWith('/documents/1')) return response(document)
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    const form = wrapper.find('.form-card form')
    const inputs = form.findAll('input')
    await inputs[2].setValue('12')
    await inputs[3].setValue('345')
    await inputs[6].setValue('100,10')
    await inputs[7].setValue('21,02')
    await inputs[8].setValue('121,12')
    await form.find('textarea').setValue('Descripción clara')
    await form.trigger('submit')
    await flushPromises()
    expect(sent).toMatchObject({ neto: '100,10', iva: '21,02', total: '121,12', moneda: 'ARS', descripcion: 'Descripción clara' })
  })

  it('conserva el id al revisar y vuelve a cargar el detalle', async () => {
    vi.spyOn(window, 'prompt').mockReturnValue('Control visual')
    let reviewed = false
    let reviewBody: Record<string, unknown> | null = null
    const fetchMock = appFetch((url, options) => {
      if (url.includes('/documents?')) return response([document])
      if (url.endsWith('/documents/1/review') && options?.method === 'POST') {
        reviewed = true
        reviewBody = JSON.parse(String(options.body))
        return response({ ...document, estado: 'reviewed', estado_nombre: 'Revisado' })
      }
      if (url.endsWith('/documents/1')) {
        return response(reviewed ? { ...document, estado: 'reviewed', estado_nombre: 'Revisado' } : document)
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    const reviewButton = wrapper.findAll('button').find((button) => button.text() === 'Marcar revisado')
    await reviewButton?.trigger('click')
    await flushPromises()
    expect(reviewed).toBe(true)
    expect(reviewBody).toEqual({ motivo: 'Control visual', expected_revision: 1 })
    expect(wrapper.text()).toContain('Revisado')
    expect(wrapper.text()).not.toContain('Ocurrió un error inesperado')
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/documents/1'))).toBe(true)
  })

  it('envía la revisión visible al dar de baja', async () => {
    vi.spyOn(window, 'prompt').mockReturnValue('Carga duplicada')
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    let deleteBody: Record<string, unknown> | null = null
    const fetchMock = appFetch((url, options) => {
      if (url.includes('/documents?')) return response([document])
      if (url.endsWith('/documents/1') && options?.method === 'DELETE') {
        deleteBody = JSON.parse(String(options.body))
        return response({ detalle: 'Comprobante dado de baja.' })
      }
      if (url.endsWith('/documents/1')) return response(document)
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Dar de baja')?.trigger('click')
    await flushPromises()

    expect(deleteBody).toEqual({ motivo: 'Carga duplicada', expected_revision: 1 })
  })

  it('borra todo el contexto privado al salir y no muestra un contexto B fallido', async () => {
    const documentB = {
      ...document,
      id: 2,
      entity_id: 2,
      outlet: { id: 20, nombre: 'Sucursal B' },
      supplier: { id: 30, razon_social: 'Proveedor B', cuit: '30722222224' },
      descripcion: 'Descripción privada B',
    }
    let currentUser = 'A'
    let failBContext = true
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/auth/csrf')) return response({ detalle: 'ok' })
      if (url.endsWith('/auth/me')) return response({ usuario: 'usuario-a' })
      if (url.endsWith('/auth/logout')) {
        currentUser = ''
        return response({ detalle: 'ok' })
      }
      if (url.endsWith('/auth/login')) {
        currentUser = 'B'
        return response({ usuario: 'usuario-b' })
      }
      if (url.endsWith('/entities')) {
        return currentUser === 'A'
          ? response([{ id: 1, nombre: 'Entidad A', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities }])
          : response([{ id: 2, nombre: 'Entidad B', cuit: '30222222223', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities }])
      }
      if (url.includes('/entities/1/outlets')) return response([{ id: 10, nombre: 'Centro A' }])
      if (url.includes('/entities/1/suppliers')) return response([{ id: 20, razon_social: 'Proveedor A', cuit: '30711111119' }])
      if (url.includes('/entities/1/documents/1')) return response({ ...document, descripcion: 'BORRADOR PRIVADO A' })
      if (url.includes('/entities/1/documents?')) return response([{ ...document, descripcion: 'BORRADOR PRIVADO A' }])
      if (url.includes('/entities/1/totals')) return response({ cantidad: 1, totales: [{ moneda: 'ARS', cantidad: 1, total: '121.12' }] })
      if (url.includes('/entities/2/outlets')) {
        return failBContext ? response({ detail: 'Contexto B no disponible.' }, 500) : response([{ id: 20, nombre: 'Sucursal B' }])
      }
      if (url.includes('/entities/2/suppliers')) return response([{ id: 30, razon_social: 'Proveedor B', cuit: '30722222224' }])
      if (url.includes('/entities/2/documents?')) return response([documentB])
      if (url.includes('/entities/2/totals')) return response({ cantidad: 1, totales: [{ moneda: 'ARS', cantidad: 1, total: '121.12' }] })
      return response({ detail: 'Ruta inesperada' }, 500)
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('BORRADOR PRIVADO A')

    await wrapper.findAll('button').find((button) => button.text() === 'Cerrar sesión')?.trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('BORRADOR PRIVADO A')
    expect(wrapper.text()).not.toContain('Proveedor A')
    expect(wrapper.text()).not.toContain('$ 121,12')

    const login = wrapper.find('.login-card form')
    await login.find('input[autocomplete="username"]').setValue('usuario-b')
    await login.find('input[type="password"]').setValue('clave-b')
    await login.trigger('submit')
    await flushPromises()
    expect(wrapper.find('.login-card').exists()).toBe(true)
    expect(wrapper.text()).toContain('Contexto B no disponible')
    expect(wrapper.text()).not.toContain('Entidad A')
    expect(wrapper.text()).not.toContain('BORRADOR PRIVADO A')

    failBContext = false
    await wrapper.find('input[autocomplete="username"]').setValue('usuario-b')
    await wrapper.find('input[type="password"]').setValue('clave-b')
    await wrapper.find('.login-card form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('Entidad B')
    expect(wrapper.text()).toContain('Proveedor B')
    expect(wrapper.text()).not.toContain('BORRADOR PRIVADO A')
  })

  it('ignora respuestas demoradas de un contexto anterior después de cambiar de cuenta', async () => {
    const delayedOutlets = deferred<Response>()
    const delayedSuppliers = deferred<Response>()
    let currentUser = 'A'
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (url.endsWith('/auth/csrf')) return response({ detalle: 'ok' })
      if (url.endsWith('/auth/me')) return response({ usuario: 'usuario-a' })
      if (url.endsWith('/auth/logout')) {
        currentUser = ''
        return response({ detalle: 'ok' })
      }
      if (url.endsWith('/auth/login')) {
        currentUser = 'B'
        return response({ usuario: 'usuario-b' })
      }
      if (url.endsWith('/entities')) {
        if (currentUser === 'B') return response([{ id: 3, nombre: 'Entidad B segura', cuit: '30333333338', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities }])
        return response([
          { id: 1, nombre: 'Entidad A inicial', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
          { id: 2, nombre: 'Entidad A demorada', cuit: '30222222223', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
        ])
      }
      if (url.includes('/entities/1/outlets')) return response([{ id: 10, nombre: 'Local A' }])
      if (url.includes('/entities/1/suppliers')) return response([{ id: 10, razon_social: 'Proveedor A', cuit: '30711111119' }])
      if (url.includes('/entities/2/outlets')) return delayedOutlets.promise
      if (url.includes('/entities/2/suppliers')) return delayedSuppliers.promise
      if (url.includes('/entities/3/outlets')) return response([{ id: 30, nombre: 'Local B seguro' }])
      if (url.includes('/entities/3/suppliers')) return response([{ id: 30, razon_social: 'Proveedor B seguro', cuit: '30733333339' }])
      if (url.includes('/entities/3/documents')) return response([])
      if (url.includes('/entities/3/totals')) return response({ cantidad: 0, totales: [] })
      if (url.includes('/documents')) return response([])
      if (url.includes('/totals')) return response({ cantidad: 0, totales: [] })
      return response({ detail: 'Ruta inesperada' }, 500)
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.context label:first-child select').setValue(2)
    await wrapper.findAll('button').find((button) => button.text() === 'Cerrar sesión')?.trigger('click')
    await flushPromises()
    const login = wrapper.find('.login-card form')
    await login.find('input[autocomplete="username"]').setValue('usuario-b')
    await login.find('input[type="password"]').setValue('clave-b')
    await login.trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('Entidad B segura')

    delayedOutlets.resolve(new Response(JSON.stringify([{ id: 99, nombre: 'LOCAL A FILTRADO' }]), { status: 200 }))
    delayedSuppliers.resolve(new Response(JSON.stringify([{ id: 99, razon_social: 'PROVEEDOR A FILTRADO', cuit: '30799999990' }]), { status: 200 }))
    await flushPromises()
    expect(wrapper.text()).toContain('Local B seguro')
    expect(wrapper.text()).not.toContain('LOCAL A FILTRADO')
    expect(wrapper.text()).not.toContain('PROVEEDOR A FILTRADO')
    expect(wrapper.text()).not.toContain('Entidad A demorada')
  })

  it('descarta una descarga privada que termina después de cerrar sesión', async () => {
    const delayedDownload = deferred<Response>()
    const createObjectURL = vi.fn(() => 'blob:privado')
    const revokeObjectURL = vi.fn()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
    const documentWithAttachment = {
      ...document,
      adjuntos: [{ id: 7, nombre: 'privado.pdf', tamano: 24 }],
    }
    vi.stubGlobal('fetch', appFetch((url) => {
      if (url.endsWith('/documents/1/attachments/7')) return delayedDownload.promise
      if (url.endsWith('/auth/logout')) return response({ detalle: 'ok' })
      if (url.includes('/documents?')) return response([documentWithAttachment])
      if (url.endsWith('/documents/1')) return response(documentWithAttachment)
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Descargar')?.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'Cerrar sesión')?.trigger('click')
    await flushPromises()

    delayedDownload.resolve(new Response('contenido privado', { status: 200 }))
    await flushPromises()
    expect(createObjectURL).not.toHaveBeenCalled()
    expect(click).not.toHaveBeenCalled()
    expect(wrapper.find('.login-card').exists()).toBe(true)
    click.mockRestore()
    vi.unstubAllGlobals()
  })

  it('libera busy al cambiar de contexto sin dejar que tareas viejas afecten una nueva', async () => {
    const firstCreate = deferred<Response>()
    const delayedSupplier = deferred<Response>()
    const currentCreate = deferred<Response>()
    let createCount = 0
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/outlets')) return response([{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }])
      if (url.endsWith('/suppliers') && options?.method === 'POST') return delayedSupplier.promise
      if (url.endsWith('/documents') && options?.method === 'POST') {
        createCount += 1
        return createCount === 1 ? firstCreate.promise : currentCreate.promise
      }
      if (url.endsWith('/documents/1')) return response(document)
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    await wrapper.find('.form-card form').trigger('submit')
    expect(wrapper.find('.form-actions button').text()).toBe('Guardando…')

    await wrapper.find('.context label:nth-child(2) select').setValue(11)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    expect(wrapper.find('.form-actions button').text()).toBe('Guardar comprobante')

    await wrapper.findAll('button').find((button) => button.text() === 'Nuevo proveedor')?.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'Guardar proveedor')?.trigger('click')
    expect(wrapper.findAll('button').find((button) => button.text() === 'Guardar proveedor')?.attributes('disabled')).toBeDefined()

    await wrapper.find('.context label:nth-child(2) select').setValue(10)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    await wrapper.find('.form-card form').trigger('submit')
    expect(wrapper.find('.form-actions button').text()).toBe('Guardando…')

    firstCreate.resolve(new Response(JSON.stringify(document), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    delayedSupplier.resolve(new Response(JSON.stringify(document.supplier), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    await flushPromises()
    expect(wrapper.find('.form-actions button').text()).toBe('Guardando…')
    expect(wrapper.find('.form-actions button').attributes('disabled')).toBeDefined()

    currentCreate.resolve(new Response(JSON.stringify(document), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    await flushPromises()
    expect(wrapper.find('.new-button').text()).toBe('Nuevo comprobante')
  })

  it('aplica el mismo control de busy a cargas de adjuntos demoradas', async () => {
    const firstUpload = deferred<Response>()
    const currentUpload = deferred<Response>()
    let uploadCount = 0
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/outlets')) return response([{ id: 10, nombre: 'Centro' }, { id: 11, nombre: 'Norte' }])
      if (url.includes('/documents?')) return response([document])
      if (url.endsWith('/documents/1/attachments') && options?.method === 'POST') {
        uploadCount += 1
        return uploadCount === 1 ? firstUpload.promise : currentUpload.promise
      }
      if (url.endsWith('/documents/1')) return response(document)
      return undefined
    }))

    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()

    async function chooseFile(name: string) {
      const input = wrapper.find('input[type="file"]')
      Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['pdf'], name, { type: 'application/pdf' })] })
      await input.trigger('change')
    }

    await chooseFile('primero.pdf')
    await wrapper.find('form.upload').trigger('submit')
    expect(wrapper.find('form.upload button').attributes('disabled')).toBeDefined()

    await wrapper.find('.context label:nth-child(2) select').setValue(11)
    await flushPromises()
    await wrapper.find('.document-row').trigger('click')
    await flushPromises()
    await chooseFile('segundo.pdf')
    expect(wrapper.find('form.upload button').attributes('disabled')).toBeUndefined()
    await wrapper.find('form.upload').trigger('submit')

    firstUpload.resolve(new Response(JSON.stringify({ id: 7 }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    await flushPromises()
    expect(wrapper.find('form.upload button').attributes('disabled')).toBeDefined()

    currentUpload.resolve(new Response(JSON.stringify({ id: 8 }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    await flushPromises()
    await chooseFile('tercero.pdf')
    expect(wrapper.find('form.upload button').attributes('disabled')).toBeUndefined()
  })

  it('muestra OCR deshabilitado y conserva la carga manual', async () => {
    vi.stubGlobal('fetch', appFetch((url) => {
      if (url.endsWith('/ocr/config')) return response({
        habilitado: false,
        proveedor: 'Gemini API',
        version_aviso: 'google-gemini-55-days-2026-10-09-v1',
        aviso: 'Aviso de privacidad.',
        mensaje: 'OCR no está habilitado, aprobado o configurado. Cargá los datos a mano y después adjuntá el archivo.',
      })
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    expect(wrapper.text()).toContain('OCR no está habilitado, aprobado o configurado')
    expect(wrapper.text()).toContain('Cargá los datos a mano')
    expect(wrapper.find('input[aria-label="Archivo para OCR"]').exists()).toBe(false)
    expect(wrapper.find('.form-grid').exists()).toBe(true)
  })

  it('no envía a Gemini al subir y exige el aviso vigente antes de pedir OCR', async () => {
    let ocrCalls = 0
    let ocrBody: Record<string, unknown> | null = null
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/ocr/config')) return response({
        habilitado: true,
        proveedor: 'Gemini API',
        version_aviso: 'google-gemini-55-days-2026-10-09-v1',
        aviso: 'El contenido del documento, los CUIT y los datos financieros se enviarán a Google mediante Gemini API. Google puede conservarlos hasta 55 días para controles de seguridad y detección de abuso.',
        mensaje: 'OCR disponible.',
      })
      if (url.endsWith('/outlets/10/intake') && options?.method === 'POST') {
        return response({ id: 'op-1', estado: 'uploaded', tipo: 'application/pdf', tamano: 10, paginas: 1, vence: '2026-10-10T00:00:00Z' }, 201)
      }
      if (url.endsWith('/intake/op-1/ocr') && options?.method === 'POST') {
        ocrCalls += 1
        ocrBody = JSON.parse(String(options.body))
        return response({
          estado: 'borrador_sin_guardar',
          borrador: {
            razon_social_proveedor: 'Proveedor Uno', cuit_emisor: '30711111111', cuit_receptor: '30111111118',
            tipo: 'invoice', letra: 'A', punto_venta: 2, numero: 9, fecha_emision: '2026-10-09', fecha_vencimiento: null,
            moneda: 'ARS', neto: null, iva: null, total: '100.00', desglose_impuestos: null,
            proveedor: { estado: 'coincidencia', coincidencia: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111111' } },
            advertencias: ['Los datos son sugerencias editables.'],
          },
        })
      }
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    const input = wrapper.find('input[aria-label="Archivo para OCR"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['sintético'], 'factura.pdf', { type: 'application/pdf' })] })
    await input.trigger('change')
    await wrapper.find('.ocr-intake form.upload').trigger('submit')
    await flushPromises()
    expect(ocrCalls).toBe(0)
    expect(wrapper.text()).toContain('se enviarán a Google mediante Gemini API')
    expect(wrapper.text()).toContain('hasta 55 días')
    const ocrButton = wrapper.findAll('button').find((button) => button.text() === 'Analizar con Gemini API')!
    expect(ocrButton.attributes('disabled')).toBeDefined()
    await wrapper.find('.ocr-consent input[type="checkbox"]').setValue(true)
    await wrapper.findAll('button').find((button) => button.text() === 'Analizar con Gemini API')!.trigger('click')
    await flushPromises()
    expect(ocrCalls).toBe(1)
    expect(ocrBody).toEqual({
      acepta_envio_google: true,
      version_aviso: 'google-gemini-55-days-2026-10-09-v1',
    })
    expect(wrapper.text()).toContain('Borrador sin guardar')
    expect(wrapper.text()).toContain('Confirmar proveedor sugerido')
    expect(wrapper.text()).toContain('CUIT emisor confirmado')
    expect(wrapper.text()).toContain('Crear otro proveedor con el CUIT emisor corregido')
  })

  it('vincula la aceptación a la versión visible e ignora configuraciones demoradas', async () => {
    const delayedConfig = deferred<Response>()
    let configCalls = 0
    let uploadCalls = 0
    let ocrBody: Record<string, unknown> | null = null
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/entities')) return response([
        { id: 1, nombre: 'Entidad Uno SA', cuit: '30111111118', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
        { id: 2, nombre: 'Entidad Dos SA', cuit: '30222222223', rol: 'administration', rol_nombre: 'Administración', capacidades: administrationCapabilities },
      ])
      if (url.includes('/entities/1/outlets')) return response([{ id: 10, nombre: 'Centro' }])
      if (url.includes('/entities/2/outlets')) return response([{ id: 20, nombre: 'Norte' }])
      if (url.includes('/entities/1/suppliers')) return response([{ id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' }])
      if (url.includes('/entities/2/suppliers')) return response([])
      if (url.endsWith('/ocr/config')) {
        configCalls += 1
        if (configCalls === 2) return delayedConfig.promise
        const version = configCalls === 1 ? 'aviso-v1' : 'aviso-v2'
        return response({
          habilitado: true,
          proveedor: 'Gemini API',
          version_aviso: version,
          aviso: `Aviso visible ${version}`,
          mensaje: 'OCR disponible.',
        })
      }
      if (url.includes('/outlets/') && url.endsWith('/intake') && options?.method === 'POST') {
        uploadCalls += 1
        return response({ id: `op-${uploadCalls}`, estado: 'uploaded', tipo: 'application/pdf', tamano: 10, paginas: 1, vence: '2026-10-10T00:00:00Z' }, 201)
      }
      if (url.includes('/intake/') && url.endsWith('/ocr') && options?.method === 'POST') {
        ocrBody = JSON.parse(String(options.body))
        return response({
          estado: 'borrador_sin_guardar',
          borrador: {
            razon_social_proveedor: null, cuit_emisor: null, cuit_receptor: null,
            tipo: null, letra: '', punto_venta: null, numero: null, fecha_emision: null, fecha_vencimiento: null,
            moneda: null, neto: null, iva: null, total: null, desglose_impuestos: null,
            proveedor: { estado: 'sin_cuit_valido', coincidencia: null }, advertencias: [],
          },
        })
      }
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()

    await wrapper.find('.new-button').trigger('click')
    let input = wrapper.find('input[aria-label="Archivo para OCR"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['uno'], 'uno.pdf', { type: 'application/pdf' })] })
    await input.trigger('change')
    await wrapper.find('.ocr-intake form.upload').trigger('submit')
    await flushPromises()
    await wrapper.find('.ocr-consent input[type="checkbox"]').setValue(true)

    await wrapper.find('.context label:first-child select').setValue(2)
    await flushPromises()
    expect(configCalls).toBe(2)
    await wrapper.find('.context label:first-child select').setValue(1)
    await flushPromises()

    await wrapper.find('.new-button').trigger('click')
    input = wrapper.find('input[aria-label="Archivo para OCR"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['dos'], 'dos.pdf', { type: 'application/pdf' })] })
    await input.trigger('change')
    await wrapper.find('.ocr-intake form.upload').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('Aviso visible aviso-v2')
    const ocrButton = wrapper.findAll('button').find((button) => button.text() === 'Analizar con Gemini API')!
    expect(ocrButton.attributes('disabled')).toBeDefined()
    await wrapper.find('.ocr-consent input[type="checkbox"]').setValue(true)
    expect(ocrButton.attributes('disabled')).toBeUndefined()

    delayedConfig.resolve(new Response(JSON.stringify({
      habilitado: true,
      proveedor: 'Gemini API',
      version_aviso: 'aviso-viejo-demorado',
      aviso: 'AVISO VIEJO DEMORADO',
      mensaje: 'OCR disponible.',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await flushPromises()
    expect(wrapper.text()).toContain('Aviso visible aviso-v2')
    expect(wrapper.text()).not.toContain('AVISO VIEJO DEMORADO')
    expect(ocrButton.attributes('disabled')).toBeUndefined()

    await wrapper.findAll('button').find((button) => button.text() === 'Analizar con Gemini API')!.trigger('click')
    await flushPromises()
    expect(ocrBody).toEqual({ acepta_envio_google: true, version_aviso: 'aviso-v2' })
  })

  it('ofrece creación explícita cuando el CUIT no tiene proveedor', async () => {
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/ocr/config')) return response({
        habilitado: true,
        proveedor: 'Gemini API',
        version_aviso: 'google-gemini-55-days-2026-10-09-v1',
        aviso: 'El contenido se enviará a Google mediante Gemini API y puede conservarse hasta 55 días.',
        mensaje: 'OCR disponible.',
      })
      if (url.endsWith('/outlets/10/intake') && options?.method === 'POST') return response({ id: 'op-2', estado: 'uploaded', tipo: 'image/png', tamano: 10, paginas: 1, vence: '2026-10-10T00:00:00Z' }, 201)
      if (url.endsWith('/intake/op-2/ocr')) return response({
        estado: 'borrador_sin_guardar',
        borrador: {
          razon_social_proveedor: 'Proveedor Nuevo', cuit_emisor: '10000000030', cuit_receptor: '30111111118',
          tipo: 'invoice', letra: 'C', punto_venta: 1, numero: 2, fecha_emision: '2026-10-09', fecha_vencimiento: null,
          moneda: 'ARS', neto: null, iva: null, total: '50.00', desglose_impuestos: null,
          proveedor: { estado: 'sin_coincidencia', coincidencia: null }, advertencias: ['Revisá el CUIT.'],
        },
      })
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.find('.new-button').trigger('click')
    const input = wrapper.find('input[aria-label="Archivo para OCR"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['foto'], 'foto.png', { type: 'image/png' })] })
    await input.trigger('change')
    await wrapper.find('.ocr-intake form.upload').trigger('submit')
    await flushPromises()
    await wrapper.find('.ocr-consent input[type="checkbox"]').setValue(true)
    await wrapper.findAll('button').find((button) => button.text() === 'Analizar con Gemini API')?.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Crear este proveedor al confirmar')
    await wrapper.find('.ocr-draft input[type="checkbox"]').setValue(true)
    expect(wrapper.find('input[maxlength="14"]').exists()).toBe(true)
    expect(wrapper.findAll('input').some((field) => (field.element as HTMLInputElement).value === 'Proveedor Nuevo')).toBe(true)
    expect(wrapper.text()).not.toContain('CUIT del proveedor')
  })

  it('crea un borrador y muestra el total aplicado antes de registrar el pago', async () => {
    const postedBodies: unknown[] = []
    const payable = {
      id: 91,
      documento_id: 1,
      referencia: 'Factura A 00001-00000002',
      outlet: { id: 10, nombre: 'Centro' },
      supplier: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' },
      fecha_emision: '2026-10-09',
      fecha_vencimiento: null,
      moneda: 'ARS',
      importe_original: '121.12',
      saldo: '121.12',
      estado: 'open',
      estado_nombre: 'Pendiente',
      vencida: false,
      registrada: '2026-10-10T00:00:00Z',
    }
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/payables')) return response([payable])
      if (url.endsWith('/payments') && (!options?.method || options.method === 'GET')) return response([])
      if (url.endsWith('/payments') && options?.method === 'POST') return response({
        id: 55,
        outlet: { id: 10, nombre: 'Centro' },
        supplier: payable.supplier,
        moneda: 'ARS',
        total: '121.12',
        metodo: 'bank_transfer',
        metodo_nombre: 'Transferencia bancaria',
        referencia: 'OP manual',
        estado: 'draft',
        estado_nombre: 'Borrador',
        tipo: 'payment',
        reversion_de: null,
        motivo_reversion: '',
        creado: '2026-10-10T00:00:00Z',
        registrado: null,
        tiene_comprobante: false,
        idempotency_key: 'server-stable-key',
      }, 201)
      if (url.endsWith('/payments/55/post')) {
        postedBodies.push(JSON.parse(options?.body as string))
        return response({
          id: 55,
          outlet: { id: 10, nombre: 'Centro' },
          supplier: payable.supplier,
          moneda: 'ARS',
          total: '121.12',
          metodo: 'bank_transfer',
          metodo_nombre: 'Transferencia bancaria',
          referencia: 'OP manual',
          estado: 'posted',
          estado_nombre: 'Registrado',
          tipo: 'payment',
          reversion_de: null,
          motivo_reversion: '',
          creado: '2026-10-10T00:00:00Z',
          registrado: '2026-10-10T00:01:00Z',
          tiene_comprobante: false,
          idempotency_key: 'server-stable-key',
        })
      }
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Cuentas a pagar')?.trigger('click')
    await flushPromises()
    const form = wrapper.find('.payment-box form.stack')
    await form.find('input[placeholder="0,00"]').setValue('121,12')
    await form.find('input[maxlength="200"]').setValue('OP manual')
    await form.trigger('submit')
    await flushPromises()
    await wrapper.find('input[placeholder="Importe a aplicar"]').setValue('121,12')
    expect(wrapper.text()).toContain('Aplicado: $ 121,12')
    await wrapper.findAll('button').find((button) => button.text() === 'Registrar pago')?.trigger('click')
    await flushPromises()
    expect(postedBodies).toEqual([{
      idempotency_key: 'server-stable-key',
      aplicaciones: [{ payable_id: 91, importe: '121,12' }],
    }])
    expect(wrapper.text()).toContain('Pago registrado')
  })

  it('reabre un borrador guardado y reutiliza su clave estable', async () => {
    const postedBodies: unknown[] = []
    const payable = {
      id: 91, documento_id: 1, referencia: 'Factura A 00001-00000002',
      outlet: { id: 10, nombre: 'Centro' },
      supplier: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' },
      fecha_emision: '2026-10-09', fecha_vencimiento: null, moneda: 'ARS',
      importe_original: '121.12', saldo: '121.12', estado: 'open', estado_nombre: 'Pendiente',
      vencida: false, registrada: '2026-10-10T00:00:00Z',
    }
    const draft = {
      id: 55, outlet: { id: 10, nombre: 'Centro' }, supplier: payable.supplier,
      moneda: 'ARS', total: '121.12', metodo: 'bank_transfer', metodo_nombre: 'Transferencia bancaria',
      referencia: 'OP guardada', estado: 'draft', estado_nombre: 'Borrador', tipo: 'payment',
      reversion_de: null, motivo_reversion: '', creado: '2026-10-10T00:00:00Z', registrado: null,
      tiene_comprobante: false, idempotency_key: 'stable-after-refresh', aplicaciones: [],
    }
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/payables')) return response([payable])
      if (url.endsWith('/payments') && (!options?.method || options.method === 'GET')) return response([draft])
      if (url.endsWith('/payments/55') && (!options?.method || options.method === 'GET')) return response(draft)
      if (url.endsWith('/payments/55/post')) {
        postedBodies.push(JSON.parse(options?.body as string))
        return response({ ...draft, estado: 'posted', estado_nombre: 'Registrado', registrado: '2026-10-10T00:01:00Z' })
      }
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Cuentas a pagar')?.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Abrir')?.trigger('click')
    await flushPromises()
    await wrapper.find('input[placeholder="Importe a aplicar"]').setValue('121,12')
    await wrapper.findAll('button').find((button) => button.text() === 'Registrar pago')?.trigger('click')
    await flushPromises()
    expect(postedBodies).toEqual([{
      idempotency_key: 'stable-after-refresh',
      aplicaciones: [{ payable_id: 91, importe: '121,12' }],
    }])
  })

  it('descarta el detalle financiero demorado al cerrar sesión', async () => {
    const delayedDetail = deferred<Response>()
    const payment = {
      id: 55, outlet: { id: 10, nombre: 'Centro' },
      supplier: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' },
      moneda: 'ARS', total: '10.00', metodo: 'cash', metodo_nombre: 'Efectivo',
      referencia: 'PRIVADO VIEJO', estado: 'draft', estado_nombre: 'Borrador', tipo: 'payment',
      reversion_de: null, motivo_reversion: '', creado: '2026-10-10T00:00:00Z', registrado: null,
      tiene_comprobante: false, idempotency_key: 'old-key', aplicaciones: [],
    }
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/payables')) return response([])
      if (url.endsWith('/payments') && (!options?.method || options.method === 'GET')) return response([payment])
      if (url.endsWith('/payments/55')) return delayedDetail.promise
      if (url.endsWith('/auth/logout')) return response({ detalle: 'ok' })
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Cuentas a pagar')?.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Abrir')?.trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === 'Cerrar sesión')?.trigger('click')
    delayedDetail.resolve(new Response(JSON.stringify(payment), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    await flushPromises()
    expect(wrapper.text()).toContain('Registro de compras')
    expect(wrapper.text()).not.toContain('PRIVADO VIEJO')
  })

  it('conserva el último pago abierto y limpia el comprobante seleccionado', async () => {
    const delayedFirst = deferred<Response>()
    let firstDetailCalls = 0
    const basePayment = {
      outlet: { id: 10, nombre: 'Centro' },
      supplier: { id: 20, razon_social: 'Proveedor Uno', cuit: '30711111119' },
      moneda: 'ARS', total: '10.00', metodo: 'cash', metodo_nombre: 'Efectivo',
      estado: 'draft', estado_nombre: 'Borrador', tipo: 'payment', reversion_de: null,
      motivo_reversion: '', creado: '2026-10-10T00:00:00Z', registrado: null,
      tiene_comprobante: false, aplicaciones: [],
    }
    const first = { ...basePayment, id: 55, referencia: 'PAGO PRIMERO', idempotency_key: 'first-key' }
    const second = { ...basePayment, id: 56, referencia: 'PAGO SEGUNDO', idempotency_key: 'second-key' }
    vi.stubGlobal('fetch', appFetch((url, options) => {
      if (url.endsWith('/payables')) return response([])
      if (url.endsWith('/payments') && (!options?.method || options.method === 'GET')) return response([first, second])
      if (url.endsWith('/payments/55')) {
        firstDetailCalls += 1
        return firstDetailCalls === 1 ? response(first) : delayedFirst.promise
      }
      if (url.endsWith('/payments/56')) return response(second)
      return undefined
    }))
    const wrapper = mount(App)
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'Cuentas a pagar')?.trigger('click')
    await flushPromises()
    const rowFor = (reference: string) => wrapper.findAll('.ap-row').find((row) => row.text().includes(reference))!
    await rowFor('PAGO PRIMERO').find('button').trigger('click')
    await flushPromises()
    const receiptInput = wrapper.find('.payment-box input[type="file"]')
    Object.defineProperty(receiptInput.element, 'files', {
      configurable: true,
      value: [new File(['evidence'], 'primero.pdf', { type: 'application/pdf' })],
    })
    await receiptInput.trigger('change')
    expect(wrapper.text()).toContain('primero.pdf')

    await rowFor('PAGO PRIMERO').find('button').trigger('click')
    await rowFor('PAGO SEGUNDO').find('button').trigger('click')
    await flushPromises()
    delayedFirst.resolve(new Response(JSON.stringify(first), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    await flushPromises()

    expect(wrapper.text()).toContain('Borrador #56')
    expect(wrapper.text()).not.toContain('Borrador #55')
    expect(wrapper.text()).not.toContain('primero.pdf')
    expect(wrapper.find('.payment-box .file-picker').text()).toContain('Adjuntar comprobante privado')
  })
})
