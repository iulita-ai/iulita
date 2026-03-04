import { ref } from 'vue'
import { api, onLogout } from '../api'

const skillMap = ref<Map<string, boolean>>(new Map())
let loaded = false
let loading: Promise<void> | null = null

// Reset on logout so next login gets fresh data.
onLogout(() => {
  skillMap.value = new Map()
  loaded = false
  loading = null
})

async function loadSkills(): Promise<void> {
  if (loaded) return
  if (loading) return loading

  loading = (async () => {
    try {
      const skills = await api.getSkills()
      const map = new Map<string, boolean>()
      for (const s of skills) {
        map.set(s.name, s.enabled)
      }
      skillMap.value = map
      loaded = true
    } catch {
      // Silently fail — sidebar just won't show conditional items
    } finally {
      loading = null
    }
  })()

  return loading
}

export function useSkillStatus() {
  // Trigger load on first use.
  if (!loaded && !loading) {
    loadSkills()
  }

  function isSkillEnabled(name: string): boolean {
    return skillMap.value.get(name) ?? false
  }

  function refresh(): Promise<void> {
    loaded = false
    return loadSkills()
  }

  return { isSkillEnabled, refresh }
}

// For testing: reset module state.
export function _resetSkillStatus(): void {
  skillMap.value = new Map()
  loaded = false
  loading = null
}
