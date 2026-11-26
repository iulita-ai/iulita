// This closed schema mirrors the server allowlist. No raw JSON or authority fields are editable.
export type ActionFieldType = 'text' | 'number' | 'boolean' | 'ids' | 'media' | 'giftTypes' | 'period'
export interface ActionField { key: string; type: ActionFieldType; required?: boolean; min?: number; max?: number; limit?: number }
export interface ActionSpec { method: string; right: string; peer: boolean; financial?: boolean; fields: ActionField[] }
const text = (key: string, limit: number, required = true): ActionField => ({ key, type: 'text', limit, required })
const number = (key: string, min = 1, max = 2147483647): ActionField => ({ key, type: 'number', min, max, required: true })
const bool = (key: string): ActionField => ({ key, type: 'boolean' })
const media = (key: string): ActionField => ({ key, type: 'media', required: true })
const spec = (method: string, right: string, peer: boolean, fields: ActionField[] = [], financial = false): ActionSpec => ({ method, right, peer, fields, financial })
const period: ActionField = { key: 'active_period', type: 'period', required: true }
export const actionSpecs: ActionSpec[] = [
  spec('sendMessage', 'can_reply', true, [text('text', 4096)]),
  ...['Photo', 'Document', 'Voice', 'Audio', 'Video'].map(kind => spec(`send${kind}`, 'can_reply', true, [media(kind.toLowerCase()), text('caption', 1024, false)])),
  spec('editMessageText', 'can_reply', true, [number('message_id'), text('text', 4096)]),
  spec('readBusinessMessage', 'can_read_messages', true, [number('message_id')]),
  ...['sent', 'all'].map(kind => spec(`delete_${kind}_business_messages`, kind === 'sent' ? 'can_delete_sent_messages' : 'can_delete_all_messages', true, [{ key: 'message_ids', type: 'ids', required: true }])),
  spec('setBusinessAccountName', 'can_edit_name', false, [text('first_name', 64), text('last_name', 64, false)]),
  spec('setBusinessAccountBio', 'can_edit_bio', false, [text('bio', 140, false)]),
  spec('setBusinessAccountUsername', 'can_edit_username', false, [text('username', 32, false)]),
  spec('setBusinessAccountProfilePhoto', 'can_edit_profile_photo', false, [media('photo'), bool('is_public')]),
  spec('removeBusinessAccountProfilePhoto', 'can_edit_profile_photo', false, [bool('is_public')]),
  spec('setBusinessAccountGiftSettings', 'can_change_gift_settings', false, [bool('show_gift_button'), { key: 'accepted_gift_types', type: 'giftTypes', required: true }]),
  spec('getBusinessAccountStarBalance', 'can_view_gifts_and_stars', false),
  spec('getBusinessAccountGifts', 'can_view_gifts_and_stars', false, [text('offset', 256, false), number('limit', 1, 100), ...['exclude_unsaved','exclude_saved','exclude_unlimited','exclude_limited_upgradable','exclude_limited_non_upgradable','exclude_unique','sort_by_price'].map(bool)]),
  spec('convertGiftToStars', 'can_convert_gifts_to_stars', false, [text('owned_gift_id', 256)], true),
  spec('deleteBusinessAccountGift', 'can_convert_gifts_to_stars', false, [text('owned_gift_id', 256)], true),
  spec('upgradeGift', 'can_transfer_and_upgrade_gifts', false, [text('owned_gift_id', 256), number('star_count', 0, 10000), bool('keep_original_details')], true),
  spec('transferGift', 'can_transfer_and_upgrade_gifts', false, [text('owned_gift_id', 256), number('new_owner_chat_id', 1, 4503599627370496), number('star_count', 0, 10000)], true),
  spec('transferBusinessAccountStars', 'can_transfer_stars', false, [number('star_count', 1, 10000)], true),
  spec('postStory', 'can_manage_stories', false, [media('content'), period, text('caption', 1024, false), bool('post_to_chat_page'), bool('protect_content')]),
  spec('editStory', 'can_manage_stories', false, [number('story_id'), media('content'), text('caption', 1024, false)]),
  spec('deleteStory', 'can_manage_stories', false, [number('story_id')]),
  spec('repostStory', 'can_manage_stories', false, [number('from_chat_id', 1, 4503599627370496), number('from_story_id'), period, bool('post_to_chat_page'), bool('protect_content')]),
]
export const giftTypes = ['unlimited_gifts', 'limited_gifts', 'unique_gifts', 'premium_subscription']
export function buildActionParams(spec: ActionSpec, values: Record<string, string | boolean>, mediaRef: string): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const field of spec.fields) {
    const value = values[field.key] ?? ''
    if (field.type === 'boolean') out[field.key] = value === true
    else if (field.type === 'giftTypes') out[field.key] = Object.fromEntries(giftTypes.map(key => [key, values[key] === true]))
    else if (field.type === 'media') {
      if (!/^bmedia_[a-zA-Z0-9_-]+$/.test(mediaRef)) throw new Error(field.key)
      out[field.key] = field.key === 'content' ? { type: 'photo', photo: mediaRef } : spec.method === 'setBusinessAccountProfilePhoto' ? { type: 'static', photo: mediaRef } : mediaRef
    } else if (field.type === 'ids') {
      const parts = String(value).trim().split(/[\s,]+/)
      if (!parts.length || parts.length > 100 || parts.some(id => !/^\d+$/.test(id))) throw new Error(field.key)
      const ids = parts.map(Number)
      if (ids.some(id => id < 1 || id > 2147483647) || new Set(ids).size !== ids.length) throw new Error(field.key)
      out[field.key] = ids
    } else if (field.type === 'number' || field.type === 'period') {
      if (!/^\d+$/.test(String(value))) throw new Error(field.key)
      const n = Number(value)
      if (!Number.isSafeInteger(n) || (field.type === 'period' ? ![21600,43200,86400,172800].includes(n) : n < field.min! || n > field.max!)) throw new Error(field.key)
      out[field.key] = n
    } else {
      const v = String(value)
      if ((field.required && !v.trim()) || v.length > field.limit! || v.includes('\0') || (field.key === 'username' && !/^[a-zA-Z0-9_]*$/.test(v))) throw new Error(field.key)
      // Empty bio/username intentionally clears that field; optional last name/caption also remain exact.
      out[field.key] = v
    }
  }
  return out
}
