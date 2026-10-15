---
layout: home
title: Iulita.ai — Персональный ИИ-ассистент
titleTemplate: false

hero:
  name: Iulita.ai
  text: ИИ-ассистент, который помнит правду
  tagline: Память на фактах. Мультиканальный. Работает локально. Открытый исходный код.
  image:
    src: /logo.svg
    alt: Iulita.ai
  actions:
    - theme: brand
      text: Быстрый старт
      link: /ru/getting-started
    - theme: alt
      text: GitHub
      link: https://github.com/iulita-ai/iulita

features:
  - icon: "\U0001F9E0"
    title: Память на фактах
    details: Хранит только проверенные факты, которые вы явно сообщили. Никаких галлюцинаций. Перекрёстные ссылки формируют инсайты.
  - icon: "\U0001F50D"
    title: Гибридный поиск
    details: Полнотекстовый FTS5 + ONNX-векторные эмбеддинги. MMR-ранжирование для разнообразных и релевантных результатов.
  - icon: "\U0001F4AC"
    title: Мультиканальный
    details: Консольный TUI, Telegram, Slack, Discord и Web Chat. Единая идентичность — факты доступны во всех каналах.
  - icon: "\U0001F916"
    title: Несколько LLM-провайдеров
    details: Claude, DeepSeek, Z.ai/GLM, OpenAI и Ollama. Проверяемые профили, роли задач, платный классификатор и резервные модели по явной policy.
  - icon: "\U0001F50C"
    title: 20+ навыков
    details: Веб-поиск, погода, Gmail, Календарь, Todoist, Craft, курсы валют, напоминания, выполнение команд, PDF и маркетплейс ClawhHub.
  - icon: "\U0001F5C4\uFE0F"
    title: Работает локально
    details: SQLite с WAL; внешние LLM и интеграции подключаются по выбору. XDG-совместимые пути, хранение секретов в keyring, установка без конфигурации.
  - icon: "\U0001F310"
    title: 6 языков
    details: Полная интернационализация на английском, русском, китайском, испанском, французском и иврите с поддержкой RTL.
  - icon: "\U0001F6E1\uFE0F"
    title: Приватность
    details: Локальное хранение памяти; выбранные внешние провайдеры получают вход для обработки. SSRF, проверяемые сессии, шифрование секретов и согласия.
---

## Актуальные руководства

- [Обзор релизов](release-notes.md)
- [API для mobile, CLI и service](mobile-api.md)
- [Профили моделей и маршрутизация (EN)](../model-routing.md)
