import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Workbench from './Workbench.vue'
import './style.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/e0' },
    { path: '/e0', name: 'e0', component: Workbench },
    { path: '/:pathMatch(.*)*', redirect: '/e0' },
  ],
})

createApp(App).use(router).mount('#app')
