import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request } from '../src/api'

describe('cliente API', () => {
  beforeEach(() => {
    Object.defineProperty(document, 'cookie', { value: 'csrftoken=token-prueba', writable: true })
  })

  it('envía sesión y CSRF en escrituras', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)
    await request('/recurso', { method: 'POST', body: '{}' })
    const [, options] = fetchMock.mock.calls[0]
    expect(options.credentials).toBe('same-origin')
    expect((options.headers as Headers).get('X-CSRFToken')).toBe('token-prueba')
  })

  it('propaga el error en español de la API', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ detail: 'Sin permiso.' }), {
      status: 403, headers: { 'Content-Type': 'application/json' },
    })))
    await expect(request('/recurso')).rejects.toEqual(new ApiError(403, 'Sin permiso.'))
  })
})
