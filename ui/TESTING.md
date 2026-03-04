# Frontend Testing Guide

## Stack

| Tool | Purpose |
|---|---|
| [Vitest](https://vitest.dev/) | Test runner (native Vite integration) |
| [@vue/test-utils](https://test-utils.vuejs.org/) | Vue component mounting and testing |
| [happy-dom](https://github.com/nicedayfor/happy-dom) | Lightweight DOM environment |
| [@vitest/coverage-v8](https://vitest.dev/guide/coverage) | Code coverage via V8 |

## Running Tests

```bash
cd ui/

# Run all tests once
npm test

# Watch mode (re-runs on file change)
npm run test:watch

# With coverage report
npm run test:coverage

# Interactive UI
npm run test:ui
```

## File Organization

Tests are co-located next to source files:

```
src/
  api.ts                    # API module
  api.test.ts               # API tests
  router.ts                 # Router config
  router.test.ts            # Router guard tests
  composables/
    useWebSocket.ts         # WebSocket composable
    useWebSocket.test.ts    # WebSocket tests
  views/
    Login.vue               # Login view
    Login.test.ts           # Login tests
    Setup.vue               # Setup wizard
    Setup.test.ts           # Setup wizard tests
  components/
    ChannelConfigForm.vue   # Channel config form
    ChannelConfigForm.test.ts
```

## Writing Tests

### API / Utility Tests

Mock `fetch` globally, test HTTP methods and token management:

```ts
import { vi, beforeEach } from 'vitest'
import { api, setTokens } from './api'

beforeEach(() => {
  localStorage.removeItem('iulita_access_token')
  localStorage.removeItem('iulita_refresh_token')
  vi.stubGlobal('fetch', vi.fn())
})
```

### Vue Component Tests

Mount components with `@vue/test-utils`. For components using `useMessage()` from Naive UI, wrap in `NMessageProvider`:

```ts
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { NMessageProvider } from 'naive-ui'
import MyComponent from './MyComponent.vue'

const Wrapper = defineComponent({
  render() {
    return h(NMessageProvider, null, {
      default: () => h(MyComponent),
    })
  },
})

const wrapper = mount(Wrapper)
const vm = wrapper.findComponent(MyComponent).vm as any
```

### Mocking the API Module

```ts
vi.mock('../api', () => ({
  api: {
    getSystem: vi.fn(),
    login: vi.fn(),
    // ... add methods as needed
  },
  setTokens: vi.fn(),
  clearTokens: vi.fn(),
}))
```

### Router Guard Tests

Create a fresh router per test with the same guard logic:

```ts
function createTestRouter() {
  const router = createRouter({ ... })
  router.beforeEach((to, _from, next) => {
    // same guard logic
  })
  return router
}
```

### WebSocket Tests

Mock the `WebSocket` class with a controllable stub:

```ts
class MockWebSocket {
  simulateOpen() { ... }
  simulateMessage(data: unknown) { ... }
  close() { ... }
  static instances: MockWebSocket[] = []
}
vi.stubGlobal('WebSocket', MockWebSocket)
```

## What to Test

- **API module**: token management, HTTP methods, auth refresh, query building
- **Router guards**: auth redirects, admin route protection
- **Composables**: WebSocket connect/disconnect/reconnect, event handling
- **Components**: rendering by props, emitted events, form validation
- **Views**: data loading on mount, user interactions, error states

## What NOT to Test

- Naive UI component internals (they have their own tests)
- CSS styling
- Static text without logic
- `main.ts` entry point

## Configuration

- `vitest.config.ts` — test runner config (happy-dom, globals, aliases)
- `vitest.setup.ts` — global mocks (localStorage, matchMedia, teleport stubs)
