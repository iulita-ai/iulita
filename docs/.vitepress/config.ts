import { defineConfig } from 'vitepress'

const version = process.env.VITEPRESS_SITE_VERSION || 'dev'

function sidebarEn() {
  return [
    {
      text: 'Introduction',
      items: [
        { text: 'Getting Started', link: '/en/getting-started' },
        { text: 'Architecture', link: '/en/architecture' },
      ]
    },
    {
      text: 'Core Concepts',
      items: [
        { text: 'Memory & Insights', link: '/en/memory-and-insights' },
        { text: 'Channels', link: '/en/channels' },
        { text: 'Skills', link: '/en/skills' },
        { text: 'Scheduler', link: '/en/scheduler' },
      ]
    },
    {
      text: 'Providers & Storage',
      items: [
        { text: 'LLM Providers', link: '/en/llm-providers' },
        { text: 'Storage', link: '/en/storage' },
      ]
    },
    {
      text: 'Operations',
      items: [
        { text: 'Configuration', link: '/en/configuration' },
        { text: 'Dashboard', link: '/en/dashboard' },
        { text: 'i18n / l10n', link: '/en/i18n' },
        { text: 'Security', link: '/en/security' },
        { text: 'Deployment', link: '/en/deployment' },
      ]
    }
  ]
}

function sidebarRu() {
  return [
    {
      text: 'Введение',
      items: [
        { text: 'Быстрый старт', link: '/ru/getting-started' },
        { text: 'Архитектура', link: '/ru/architecture' },
      ]
    },
    {
      text: 'Основные концепции',
      items: [
        { text: 'Память и инсайты', link: '/ru/memory-and-insights' },
        { text: 'Каналы', link: '/ru/channels' },
        { text: 'Навыки', link: '/ru/skills' },
        { text: 'Планировщик', link: '/ru/scheduler' },
      ]
    },
    {
      text: 'Провайдеры и хранилище',
      items: [
        { text: 'LLM-провайдеры', link: '/ru/llm-providers' },
        { text: 'Хранилище', link: '/ru/storage' },
      ]
    },
    {
      text: 'Эксплуатация',
      items: [
        { text: 'Конфигурация', link: '/ru/configuration' },
        { text: 'Дашборд', link: '/ru/dashboard' },
        { text: 'i18n / l10n', link: '/ru/i18n' },
        { text: 'Безопасность', link: '/ru/security' },
        { text: 'Развёртывание', link: '/ru/deployment' },
      ]
    }
  ]
}

function sidebarZh() {
  return [
    {
      text: '入门',
      items: [
        { text: '快速开始', link: '/zh/getting-started' },
        { text: '架构', link: '/zh/architecture' },
      ]
    },
    {
      text: '核心概念',
      items: [
        { text: '记忆与洞察', link: '/zh/memory-and-insights' },
        { text: '频道', link: '/zh/channels' },
        { text: '技能', link: '/zh/skills' },
        { text: '调度器', link: '/zh/scheduler' },
      ]
    },
    {
      text: '提供者与存储',
      items: [
        { text: 'LLM 提供者', link: '/zh/llm-providers' },
        { text: '存储', link: '/zh/storage' },
      ]
    },
    {
      text: '运维',
      items: [
        { text: '配置', link: '/zh/configuration' },
        { text: '仪表板', link: '/zh/dashboard' },
        { text: '国际化', link: '/zh/i18n' },
        { text: '安全', link: '/zh/security' },
        { text: '部署', link: '/zh/deployment' },
      ]
    }
  ]
}

function sidebarEs() {
  return [
    {
      text: 'Introducción',
      items: [
        { text: 'Primeros pasos', link: '/es/getting-started' },
        { text: 'Arquitectura', link: '/es/architecture' },
      ]
    },
    {
      text: 'Conceptos clave',
      items: [
        { text: 'Memoria e insights', link: '/es/memory-and-insights' },
        { text: 'Canales', link: '/es/channels' },
        { text: 'Habilidades', link: '/es/skills' },
        { text: 'Planificador', link: '/es/scheduler' },
      ]
    },
    {
      text: 'Proveedores y almacenamiento',
      items: [
        { text: 'Proveedores LLM', link: '/es/llm-providers' },
        { text: 'Almacenamiento', link: '/es/storage' },
      ]
    },
    {
      text: 'Operaciones',
      items: [
        { text: 'Configuración', link: '/es/configuration' },
        { text: 'Dashboard', link: '/es/dashboard' },
        { text: 'i18n / l10n', link: '/es/i18n' },
        { text: 'Seguridad', link: '/es/security' },
        { text: 'Despliegue', link: '/es/deployment' },
      ]
    }
  ]
}

function sidebarFr() {
  return [
    {
      text: 'Introduction',
      items: [
        { text: 'Démarrage rapide', link: '/fr/getting-started' },
        { text: 'Architecture', link: '/fr/architecture' },
      ]
    },
    {
      text: 'Concepts clés',
      items: [
        { text: 'Mémoire et insights', link: '/fr/memory-and-insights' },
        { text: 'Canaux', link: '/fr/channels' },
        { text: 'Compétences', link: '/fr/skills' },
        { text: 'Planificateur', link: '/fr/scheduler' },
      ]
    },
    {
      text: 'Fournisseurs et stockage',
      items: [
        { text: 'Fournisseurs LLM', link: '/fr/llm-providers' },
        { text: 'Stockage', link: '/fr/storage' },
      ]
    },
    {
      text: 'Opérations',
      items: [
        { text: 'Configuration', link: '/fr/configuration' },
        { text: 'Tableau de bord', link: '/fr/dashboard' },
        { text: 'i18n / l10n', link: '/fr/i18n' },
        { text: 'Sécurité', link: '/fr/security' },
        { text: 'Déploiement', link: '/fr/deployment' },
      ]
    }
  ]
}

function sidebarHe() {
  return [
    {
      text: 'מבוא',
      items: [
        { text: 'התחלה מהירה', link: '/he/getting-started' },
        { text: 'ארכיטקטורה', link: '/he/architecture' },
      ]
    },
    {
      text: 'מושגי יסוד',
      items: [
        { text: 'זיכרון ותובנות', link: '/he/memory-and-insights' },
        { text: 'ערוצים', link: '/he/channels' },
        { text: 'כישורים', link: '/he/skills' },
        { text: 'מתזמן', link: '/he/scheduler' },
      ]
    },
    {
      text: 'ספקים ואחסון',
      items: [
        { text: 'ספקי LLM', link: '/he/llm-providers' },
        { text: 'אחסון', link: '/he/storage' },
      ]
    },
    {
      text: 'תפעול',
      items: [
        { text: 'תצורה', link: '/he/configuration' },
        { text: 'לוח בקרה', link: '/he/dashboard' },
        { text: 'בינלאומיות', link: '/he/i18n' },
        { text: 'אבטחה', link: '/he/security' },
        { text: 'פריסה', link: '/he/deployment' },
      ]
    }
  ]
}

export default defineConfig({
  title: 'Iulita.ai',
  description: 'Personal AI assistant with fact-based memory',
  lastUpdated: true,
  ignoreDeadLinks: [
    /config\.toml\.example/,
  ],

  sitemap: {
    hostname: 'https://iulita.ai'
  },

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/logo.svg' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '192x192', href: '/logo-192x192.png' }],
    ['meta', { name: 'theme-color', content: '#6C3FC7' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: 'Iulita.ai' }],
    ['meta', { property: 'og:image', content: 'https://iulita.ai/logo-192x192.png' }],
  ],

  themeConfig: {
    logo: '/logo.svg',
    siteTitle: 'Iulita.ai',

    nav: [
      { text: version, link: `https://github.com/iulita-ai/iulita/releases/tag/${version}` }
    ],

    socialLinks: [
      { icon: 'github', link: 'https://github.com/iulita-ai/iulita' }
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2025-2026 Stanislav Gumeniuk'
    },

    search: {
      provider: 'local'
    }
  },

  locales: {
    en: {
      label: 'English',
      lang: 'en-US',
      themeConfig: {
        nav: [
          { text: 'Docs', link: '/en/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: {
          '/en/': sidebarEn()
        },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: 'Edit this page on GitHub'
        }
      }
    },
    ru: {
      label: 'Русский',
      lang: 'ru-RU',
      themeConfig: {
        nav: [
          { text: 'Документация', link: '/ru/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: { '/ru/': sidebarRu() },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: 'Редактировать на GitHub'
        }
      }
    },
    zh: {
      label: '中文',
      lang: 'zh-CN',
      themeConfig: {
        nav: [
          { text: '文档', link: '/zh/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: { '/zh/': sidebarZh() },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: '在 GitHub 上编辑此页'
        }
      }
    },
    es: {
      label: 'Español',
      lang: 'es-ES',
      themeConfig: {
        nav: [
          { text: 'Documentación', link: '/es/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: { '/es/': sidebarEs() },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: 'Editar en GitHub'
        }
      }
    },
    fr: {
      label: 'Français',
      lang: 'fr-FR',
      themeConfig: {
        nav: [
          { text: 'Documentation', link: '/fr/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: { '/fr/': sidebarFr() },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: 'Modifier sur GitHub'
        }
      }
    },
    he: {
      label: 'עברית',
      lang: 'he-IL',
      dir: 'rtl',
      themeConfig: {
        nav: [
          { text: 'תיעוד', link: '/he/getting-started' },
          { text: 'GitHub', link: 'https://github.com/iulita-ai/iulita' }
        ],
        sidebar: { '/he/': sidebarHe() },
        editLink: {
          pattern: 'https://github.com/iulita-ai/iulita/edit/main/docs/:path',
          text: 'ערוך בגיטהאב'
        }
      }
    }
  }
})
