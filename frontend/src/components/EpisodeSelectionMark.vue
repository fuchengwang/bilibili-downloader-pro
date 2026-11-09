<script setup lang="ts">
import { Check, CircleHelp, Ellipsis, LockKeyhole, Minus } from 'lucide-vue-next'
import type { EpisodeAccess } from '../utils/episode-playback'

defineProps<{
  state?: EpisodeAccess['state']
  checked?: boolean | 'mixed'
}>()
</script>

<template>
  <span class="selection-mark" :class="[state, { checked }]" aria-hidden="true">
    <LockKeyhole v-if="state === 'blocked'" :size="14" />
    <Ellipsis v-else-if="state === 'checking'" :size="16" />
    <Check v-else-if="checked === true" :size="14" :stroke-width="2.8" />
    <Minus v-else-if="checked === 'mixed'" :size="14" :stroke-width="2.8" />
    <CircleHelp v-else-if="state === 'unknown'" :size="13" />
  </span>
</template>

<style scoped>
.selection-mark {
  display: grid;
  place-items: center;
  flex: 0 0 20px;
  width: 20px;
  height: 20px;
  box-sizing: border-box;
  border: 1.5px solid var(--text-muted);
  border-radius: 5px;
  color: var(--bili-pink);
}

.selection-mark.checked {
  border-color: var(--bili-pink);
  background: var(--bili-pink);
  color: var(--bg-app);
}

.selection-mark.blocked {
  border-color: transparent;
  border-radius: 50%;
  color: var(--guest-accent);
  background: var(--guest-login-bg);
}

.selection-mark.checking, .selection-mark.unknown:not(.checked) {
  color: var(--text-muted);
}

.selection-mark.checking, .selection-mark.unknown:not(.checked) { border-color: var(--neutral-25); }
</style>
