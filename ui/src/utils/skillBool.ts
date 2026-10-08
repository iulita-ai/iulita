// Boolean-ish skill config key helpers, shared between the Settings drawer
// and its tests. Kept in a pure module so the normalization contracts can be
// pinned co-located (the Go side defines them; see tests for the mirror).

/**
 * Keys rendered as a switch in the per-skill config drawer.
 * - `*.enabled`  — registry skill toggles (registry.go DispatchConfigChanged)
 * - `*_enabled`  — feature kill-switches (e.g. geolocation geocode)
 */
export function isSkillBoolKey(key: string): boolean {
  return key.endsWith('.enabled') || key.endsWith('_enabled')
}

/**
 * Switch state for a boolean-ish key, mirroring the Go normalizers exactly:
 * - `_enabled` kill-switches (isGeocodeDisabledValue): trims, lowercases;
 *   false/0/no/off disable; anything else (incl. unset) enables.
 * - `.enabled` registry toggles (DispatchConfigChanged): NO trim — only the
 *   exact ''/'true' (case-insensitive)/'1' enable; padded values stay OFF.
 */
export function skillBoolValue(key: string, editValue: string | undefined, storedValue: string | undefined): boolean {
  const raw = editValue ?? storedValue ?? ''
  if (key.endsWith('_enabled')) {
    const v = raw.trim().toLowerCase()
    if (v === '') return true
    return !['false', '0', 'no', 'off'].includes(v)
  }
  return raw === '' || raw.toLowerCase() === 'true' || raw === '1'
}
