// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §4/§8). The third Vite entry: its own router and
// locale table. It does not load App.vue, the main router guards, utils/request.ts, any store or the
// TDesign bundle, and it reads or writes no storage.

import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import KnowledgeBaseDetail from './views/KnowledgeBaseDetail.vue'
import KnowledgeBaseList from './views/KnowledgeBaseList.vue'
import { locale, pickLocale } from './i18n'
import { bridge } from './page'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/knowledge-bases', name: 'kb-list', component: KnowledgeBaseList },
    { path: '/knowledge-bases/:kbId', name: 'kb-detail', component: KnowledgeBaseDetail },
    { path: '/:rest(.*)*', redirect: { name: 'kb-list' } },
  ],
})

bridge.onState((s) => {
  document.documentElement.dataset.theme = s.theme
  locale.value = pickLocale(s.locale)
})
bridge.start()

createApp(App).use(router).mount('#embed-admin')
