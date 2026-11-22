import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ThemeSelect from '../src/components/ThemeSelect.vue'
import { APP_THEME, appTheme } from '../src/theme'
import { appInstall } from '../src/appInstall'

afterEach(() => {
  window.localStorage.clear()
  window.history.replaceState({}, '', '/')
  document.documentElement.removeAttribute('data-theme')
  document.querySelector('link[rel="manifest"]')?.remove()
})

describe('appearance', () => {
  it('applies explicit choices, remembers on reload, and clears for system', async () => {
    window.localStorage.setItem('gomar.theme', 'dark')
    const theme = appTheme(window)
    expect(theme.mode.value).toBe('dark')
    const panel = mount(ThemeSelect, { global: { provide: { [APP_THEME as symbol]: theme } } })
    expect(document.documentElement.dataset.theme).toBe('dark')
    await panel.get('select').setValue('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(appTheme(window).mode.value).toBe('light')
    await panel.get('select').setValue('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(window.localStorage.getItem('gomar.theme')).toBeNull()
    panel.unmount()
  })

  it('ignores invalid stored values and applies cross-tab changes', () => {
    window.localStorage.setItem('gomar.theme', 'nocturnal')
    const theme = appTheme(window)
    expect(theme.mode.value).toBe('system')
    window.dispatchEvent(new StorageEvent('storage', { key: 'gomar.theme', newValue: 'dark' }))
    expect(theme.mode.value).toBe('dark')
    window.dispatchEvent(new StorageEvent('storage', { key: 'gomar.theme', newValue: null }))
    expect(theme.mode.value).toBe('system')
  })
})

describe('staff installation', () => {
  it('adds the staff manifest and consumes a native prompt only once', async () => {
    const install = appInstall(window)
    const manifest = document.head.querySelector('link[rel="manifest"]')
    expect(manifest?.getAttribute('href')).toBe('/static/pwa/gomar.webmanifest')
    expect(install.available.value).toBe(false)
    const prompt = vi.fn(async () => undefined)
    const nativeEvent = Object.assign(new Event('beforeinstallprompt', { cancelable: true }), { prompt })
    window.dispatchEvent(nativeEvent)
    expect(nativeEvent.defaultPrevented).toBe(true)
    expect(install.available.value).toBe(true)
    await install.install()
    await install.install()
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(install.available.value).toBe(false)
  })

  it.each(['/reservas/local', '/pos', '/admin/'])('does not advertise installation on %s', (path) => {
    window.history.replaceState({}, '', path)
    expect(appInstall(window).available.value).toBe(false)
    expect(document.head.querySelector('link[rel="manifest"]')).toBeNull()
  })
})
