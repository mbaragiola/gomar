import { createApp, defineComponent, h, shallowRef } from 'vue'
import App from './App.vue'
import PosApp from './pos/PosApp.vue'
import ReconciliationCloseHandoff from './components/ReconciliationCloseHandoff.vue'
import { isReconciliationCloseHandoff } from './reconciliation-close-handoff'
import { APP_THEME, appTheme } from './theme'
import { APP_INSTALL, appInstall } from './appInstall'
import './style.css'

const address = () => ({
  hash: window.location.hash,
  path: window.location.pathname,
  search: window.location.search,
})
const route = shallowRef(address())
function updateRoute() { route.value = address() }
window.addEventListener('hashchange', updateRoute)
window.addEventListener('popstate', updateRoute)

const Root = defineComponent(() => () => {
  const { path, hash, search } = route.value
  // Choose the isolated shell before mounting anything that loads staff data.
  if (isReconciliationCloseHandoff(path, hash)) return h(ReconciliationCloseHandoff, { hash, search })
  return h(path === '/pos' ? PosApp : App)
})

createApp(Root)
  .provide(APP_THEME, appTheme(window))
  .provide(APP_INSTALL, appInstall(window))
  .mount('#app')
