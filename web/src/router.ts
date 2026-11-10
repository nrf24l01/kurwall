import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('@/pages/LoginPage.vue'), meta: { title: 'Sign in', public: true } },
    { path: '/', name: 'overview', component: () => import('@/pages/OverviewPage.vue'), meta: { title: 'Overview', keys: 'r refresh · / search' } },
    { path: '/nodes', name: 'nodes', component: () => import('@/pages/NodesPage.vue'), meta: { title: 'Nodes', keys: '↵ open · j k move · r refresh' } },
    { path: '/nodes/:id', name: 'node', component: () => import('@/pages/NodePage.vue'), meta: { title: 'Node', keys: 'r refresh · / search' } },
    { path: '/policies', name: 'policies', component: () => import('@/pages/PoliciesPage.vue'), meta: { title: 'Policies', keys: 'n new · ↵ open · j k move · r refresh' } },
    { path: '/asns', name: 'asns', component: () => import('@/pages/AsnsPage.vue'), meta: { title: 'ASNs', keys: 'space select · ↵ add rule · j k move · r refresh' } },
    { path: '/policies/:id', name: 'policy', component: () => import('@/pages/PolicyPage.vue'), meta: { title: 'Policy', keys: 'r refresh · / search' } },
    { path: '/events', name: 'events', component: () => import('@/pages/EventsPage.vue'), meta: { title: 'Events', keys: 'r refresh · / search' } },
    { path: '/settings', name: 'settings', component: () => import('@/pages/SettingsPage.vue'), meta: { title: 'Settings', keys: 'r refresh · / search' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})
