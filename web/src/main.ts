import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import Workbench from './Workbench.vue'
import WorkspaceShell from './WorkspaceShell.vue'
import OverviewPage from './pages/OverviewPage.vue'
import InventoryPage from './pages/InventoryPage.vue'
import AuditsPage from './pages/AuditsPage.vue'
import FindingsPage from './pages/FindingsPage.vue'
import ReportsPage from './pages/ReportsPage.vue'
import './style.css'
import './workspace.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/', component: WorkspaceShell, children: [
      { path: 'overview', name: 'overview', component: OverviewPage },
      { path: 'inventory/:id?', name: 'inventory', component: InventoryPage },
      { path: 'audits/:id?', name: 'audits', component: AuditsPage },
      { path: 'findings', name: 'findings', component: FindingsPage },
      { path: 'reports/:id?', name: 'reports', component: ReportsPage },
    ] },
    { path: '/e0', name: 'e0', component: Workbench },
    { path: '/:pathMatch(.*)*', redirect: '/overview' },
  ],
})

createApp(App).use(router).mount('#app')
