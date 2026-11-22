import { createApp } from 'vue'
import App from './App.vue'
import PosApp from './pos/PosApp.vue'
import { APP_THEME, appTheme } from './theme'
import { APP_INSTALL, appInstall } from './appInstall'
import './style.css'

createApp(window.location.pathname === '/pos' ? PosApp : App)
  .provide(APP_THEME, appTheme(window))
  .provide(APP_INSTALL, appInstall(window))
  .mount('#app')
