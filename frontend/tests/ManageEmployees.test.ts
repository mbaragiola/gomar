import { flushPromises, mount } from '@vue/test-utils'
import { expect, it, vi } from 'vitest'
import ManageEmployees from '../src/components/ManageEmployees.vue'

it('manages an older employee and returns to the first page', async () => {
  const path = '/api/v1/entities/12/employees'
  const old = { id: 9, username: 'turno-anterior', role: 'purchasing', status: 'active' }
  const recent = { ...old, id: 200, username: 'turno-reciente' }
  const respond = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body)))
  const list = vi.fn((url: string, options: RequestInit) => {
    if (url === path) return respond({ employees: [recent], next_cursor: 'a-200' })
    if (url === path + '?before=a-200') return respond({ employees: [old] })
    if (url === path + '/9/reset' && options.method === 'POST') {
      return respond({ id: 9, organization: 12, username: old.username, temporary_password: 'a'.repeat(32), expires_at: '2026-12-01T08:30:00Z' })
    }
    if (url === path + '/9/disable' && options.method === 'POST') {
      old.status = 'disabled'
      return respond({})
    }
    throw new Error(`Unexpected request: ${url}`)
  })
  vi.stubGlobal('fetch', list)
  const page = mount(ManageEmployees, { props: { organization: 12, outlets: [] } })
  const click = async (text: string) => {
    await page.findAll('button').find((button) => button.text() === text)!.trigger('click')
    await flushPromises()
  }
  try {
    await flushPromises()
    expect(page.text()).not.toContain(old.username)
    await click('Más empleados')
    await click('Reemitir contraseña de turno-anterior')
    await click('Confirmar reemplazo de contraseña')
    expect(page.text()).toContain('Mostrar contraseña temporal')
    await click('Desactivar a turno-anterior')
    await click('Confirmar desactivación')
    expect(page.text()).toContain('turno-anterior · Compras · Desactivado')
    expect(page.text()).not.toContain('Reemitir contraseña de turno-anterior')
    expect(list.mock.calls.filter(([url]) => url === path + '?before=a-200')).toHaveLength(3)
    await click('Empleados anteriores')
    expect(page.text()).toContain('turno-reciente')
    expect(page.text()).not.toContain('turno-anterior')
  } finally {
    page.unmount()
    vi.unstubAllGlobals()
  }
})
