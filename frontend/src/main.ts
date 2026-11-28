import { createApp, defineComponent, h, shallowRef } from 'vue'
import App from './App.vue'
import { pendingSignOut, syncPendingSignOut } from './session-revocation'
import PosRoute from './pos/PosRoute.vue'
import NativeSignup from './signup/NativeSignup.vue'
import ReconciliationCloseHandoff from './components/ReconciliationCloseHandoff.vue'
import { isReconciliationCloseHandoff } from './reconciliation-close-handoff'
import PayableEligibilityHandoff from './components/PayableEligibilityHandoff.vue'
import { isPayableEligibilityHandoff } from './payable-eligibility-handoff'
import { APP_THEME, appTheme } from './theme'
import { APP_INSTALL, appInstall } from './appInstall'
import './style.css'

const address = () => ({
  hash: window.location.hash,
  path: window.location.pathname,
  search: window.location.search,
  emptyQuery: window.location.href.split('#', 1)[0].endsWith('?'),
})
const route = shallowRef(address())
function updateRoute() { route.value = address() }
window.addEventListener('hashchange', updateRoute)
window.addEventListener('popstate', updateRoute)
window.addEventListener('storage', syncPendingSignOut)

const Root = defineComponent(() => () => {
  const { path, hash, search, emptyQuery } = route.value
  if (pendingSignOut.value) return h(App)
  // Choose the isolated shell before mounting anything that loads staff data.
  if (path === '/alta') return h(NativeSignup, { key: `${search}:${hash}:${emptyQuery}`, validAddress: !hash && !search && !emptyQuery })
  if (isPayableEligibilityHandoff(path, hash)) return h(PayableEligibilityHandoff, { hash, search: search || (emptyQuery ? '?' : '') })
  if (isReconciliationCloseHandoff(path, hash)) return h(ReconciliationCloseHandoff, { hash, search })
  return path === '/pos' ? h(PosRoute, { hash, search, emptyQuery }) : h(App)
})

createApp(Root)
  .provide(APP_THEME, appTheme(window))
  .provide(APP_INSTALL, appInstall(window))
  .mount('#app')
