import { createRouter, createWebHistory } from 'vue-router'
import { isLoggedIn, isAdmin } from './api'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('./views/Login.vue'),
      meta: { public: true },
    },
    {
      path: '/',
      name: 'dashboard',
      component: () => import('./views/Dashboard.vue'),
    },
    {
      path: '/facts',
      name: 'facts',
      component: () => import('./views/Facts.vue'),
    },
    {
      path: '/insights',
      name: 'insights',
      component: () => import('./views/Insights.vue'),
    },
    {
      path: '/reminders',
      name: 'reminders',
      component: () => import('./views/Reminders.vue'),
    },
    {
      path: '/profile',
      name: 'profile',
      component: () => import('./views/TechFacts.vue'),
    },
    {
      path: '/usage',
      name: 'usage',
      component: () => import('./views/Usage.vue'),
      meta: { admin: true },
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('./views/Settings.vue'),
    },
    {
      path: '/users',
      name: 'users',
      component: () => import('./views/Users.vue'),
      meta: { admin: true },
    },
    {
      path: '/channels',
      name: 'channels',
      component: () => import('./views/Channels.vue'),
      meta: { admin: true },
    },
    {
      path: '/agent-jobs',
      name: 'agent-jobs',
      component: () => import('./views/AgentJobs.vue'),
      meta: { admin: true },
    },
    {
      path: '/chat',
      name: 'chat',
      component: () => import('./views/Chat.vue'),
    },
    {
      path: '/tasks',
      name: 'tasks',
      component: () => import('./views/Tasks.vue'),
    },
    {
      path: '/setup',
      name: 'setup',
      component: () => import('./views/Setup.vue'),
      meta: { admin: true },
    },
    {
      path: '/skills',
      name: 'skills',
      component: () => import('./views/ExternalSkills.vue'),
      meta: { admin: true },
    },
    {
      path: '/config-debug',
      name: 'config-debug',
      component: () => import('./views/ConfigDebug.vue'),
      meta: { admin: true },
    },
  ],
})

router.beforeEach((to, _from, next) => {
  if (to.meta.public) {
    next()
    return
  }
  if (!isLoggedIn()) {
    next({ name: 'login' })
    return
  }
  if (to.meta.admin && !isAdmin()) {
    next({ name: 'dashboard' })
    return
  }
  next()
})

export default router
