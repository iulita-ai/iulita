<template>
  <Layout v-if="showLayout">
    <router-view />
  </Layout>
  <router-view v-else />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import Layout from './components/Layout.vue'

const route = useRoute()

const showLayout = computed(() => {
  // Hide layout on public routes (login) and during initial route resolution.
  if (!route.name || route.meta?.public) return false
  return true
})
</script>
