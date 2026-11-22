import { computed, shallowRef, type InjectionKey } from 'vue'

const manifestURL = '/static/pwa/gomar.webmanifest'
type NativeInstallPrompt = Event & { prompt(): Promise<unknown> }

function isPrompt(event: Event): event is NativeInstallPrompt {
  return 'prompt' in event && typeof event.prompt === 'function'
}

export function appInstall(browser: Window) {
  const prompt = shallowRef<NativeInstallPrompt | null>(null)
  const installed = shallowRef(false)
  const staffRoot = browser.location.pathname === '/'
  const standalone = () => browser.matchMedia?.('(display-mode: standalone)').matches === true

  if (staffRoot) {
    browser.addEventListener('beforeinstallprompt', (event) => {
      if (installed.value || standalone() || !isPrompt(event)) return
      event.preventDefault()
      prompt.value = event
    })
    browser.addEventListener('appinstalled', () => {
      installed.value = true
      prompt.value = null
    })
    const head = browser.document.head
    if (!head.querySelector('link[rel="manifest"]')) {
      const manifest = browser.document.createElement('link')
      manifest.rel = 'manifest'
      manifest.href = manifestURL
      head.append(manifest)
    }
  }
  return {
    available: computed(() => prompt.value !== null && !installed.value),
    async install() {
      const ready = prompt.value
      prompt.value = null
      if (!ready || standalone()) return
      try {
        await ready.prompt()
      } catch {
        // The browser may withdraw a prompt. Its own menu is independent.
      }
    },
  }
}

export const APP_INSTALL: InjectionKey<ReturnType<typeof appInstall>> = Symbol('app install')
