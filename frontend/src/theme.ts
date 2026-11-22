import { readonly, ref, type InjectionKey } from 'vue'
import { themeKey } from '../theme-bootstrap'

export type ThemeMode = 'system' | 'light' | 'dark'

function parseTheme(value: string | null): ThemeMode {
  return value === 'light' || value === 'dark' ? value : 'system'
}

export function appTheme(browser: Window) {
  const mode = ref<ThemeMode>('system')
  function apply(value: ThemeMode) {
    mode.value = value
    const data = browser.document.documentElement.dataset
    if (value === 'system') delete data.theme
    else data.theme = value
  }
  try {
    apply(parseTheme(browser.localStorage.getItem(themeKey)))
  } catch {
    apply('system')
  }
  browser.addEventListener('storage', (event) => {
    if (event.key === themeKey || event.key === null) {
      apply(parseTheme(event.newValue))
    }
  })
  return {
    mode: readonly(mode),
    set(value: ThemeMode) {
      apply(value)
      try {
        if (value === 'system') browser.localStorage.removeItem(themeKey)
        else browser.localStorage.setItem(themeKey, value)
      } catch {
        // The current page can still change theme if storage is blocked.
      }
    },
  }
}

export const APP_THEME: InjectionKey<ReturnType<typeof appTheme>> = Symbol('app theme')
