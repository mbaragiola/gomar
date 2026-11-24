export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}
function cookie(name: string): string {
  const value = document.cookie.split('; ').find((item) => item.startsWith(`${name}=`))
  return value ? decodeURIComponent(value.split('=').slice(1).join('=')) : ''
}

export async function request<T>(path: string, options: RequestInit = {}, captureError?: (body: unknown) => void): Promise<T> {
  const headers = new Headers(options.headers)
  if (!(options.body instanceof FormData) && options.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (options.method && options.method !== 'GET') {
    headers.set('X-CSRFToken', cookie('csrftoken'))
  }
  const response = await fetch(`/api/v1${path}`, { ...options, headers, credentials: 'same-origin' })
  if (!response.ok) {
    let message = 'No se pudo completar la operación.'
    try {
      const payload = await response.json() as { detail?: unknown } | null
      captureError?.(payload)
      if (typeof payload?.detail === 'string') message = payload.detail
    } catch {
      // Se conserva el mensaje en español para respuestas no JSON.
    }
    throw new ApiError(response.status, message)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export async function download(path: string, filename: string, isCurrent: () => boolean): Promise<void> {
  if (!isCurrent()) return
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin' })
  if (!response.ok) throw new ApiError(response.status, 'No se pudo descargar el archivo.')
  if (!isCurrent()) return
  const blob = await response.blob()
  if (!isCurrent()) return
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  if (isCurrent()) link.click()
  URL.revokeObjectURL(url)
}
