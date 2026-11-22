import { createApp } from 'vue'
import App from './App.vue'
import PosApp from './pos/PosApp.vue'
import './style.css'

createApp(window.location.pathname === '/pos' ? PosApp : App).mount('#app')
