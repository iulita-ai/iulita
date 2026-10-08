import { describe, expect, it } from 'vitest'

import { isSkillBoolKey, skillBoolValue } from './skillBool'

describe('isSkillBoolKey', () => {
  it('accepts registry toggles and feature kill-switches', () => {
    expect(isSkillBoolKey('skills.insights.enabled')).toBe(true)
    expect(isSkillBoolKey('skills.geolocation.geocode_enabled')).toBe(true)
  })

  it('rejects regular keys', () => {
    expect(isSkillBoolKey('skills.web.api_key')).toBe(false)
    expect(isSkillBoolKey('skills.craft.system_prompt')).toBe(false)
  })
})

describe('skillBoolValue — _enabled kill-switch family (mirrors isGeocodeDisabledValue)', () => {
  const key = 'skills.geolocation.geocode_enabled'

  it.each([
    ['', true], // unset default: enabled
    ['true', true],
    ['1', true],
    ['yes', true],
    ['false', false],
    ['False', false],
    ['0', false],
    ['no', false],
    ['off', false],
    ['OFF', false],
    [' false ', false], // trimmed
  ])('value %o → %o', (value, expected) => {
    expect(skillBoolValue(key, undefined, value)).toBe(expected)
  })

  it('prefers the pending edit over the stored value', () => {
    expect(skillBoolValue(key, 'false', 'true')).toBe(false)
    expect(skillBoolValue(key, 'true', 'false')).toBe(true)
  })
})

describe('skillBoolValue — .enabled registry family (registry DispatchConfigChanged)', () => {
  const key = 'skills.insights.enabled'

  it.each([
    ['', true], // absent enables (registry quirk)
    ['true', true],
    ['TRUE', true], // EqualFold
    ['1', true],
    ['0', false],
    ['false', false],
    ['yes', false], // only true/1 enable
    ['off', false],
    [' true ', false], // registry does NOT trim: padded stays disabled
    [' 1', false],
  ])('value %o → %o', (value, expected) => {
    expect(skillBoolValue(key, undefined, value)).toBe(expected)
  })
})
