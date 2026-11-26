import { describe, expect, it } from 'vitest'
import i18n from './i18n'
import { actionSpecs, buildActionParams } from './businessActions'
const spec = (method: string) => actionSpecs.find(item => item.method === method)!
describe('Typed business operation parameters', () => {
  it.each(['en','ru'] as const)('renders every operation and field label without breaking the %s form', locale => {
    i18n.global.locale.value=locale
    for(const spec of actionSpecs) {
      expect(i18n.global.t(`secretaryActions.methods.${spec.method}`)).not.toContain('secretaryActions.')
      for(const field of spec.fields) expect(i18n.global.t(`secretaryActions.fields.${field.key}`)).not.toContain('secretaryActions.')
    }
    expect(i18n.global.t('secretaryActions.fields.username')).toContain('@')
  })
  it('has explicit forms for all fourteen independent rights', () => {
    expect(new Set(actionSpecs.map(item => item.right)).size).toBe(14)
    expect(new Set(actionSpecs.map(item => item.method)).size).toBe(actionSpecs.length)
  })
  it('does not allow a text value to inject authority or a second recipient', () => {
    expect(buildActionParams(spec('sendMessage'), { text: '{"business_connection_id":"another","chat_id":9}', chat_id: '99' }, '')).toEqual({ text: '{"business_connection_id":"another","chat_id":9}' })
    expect(() => buildActionParams(spec('transferGift'), { owned_gift_id: 'gift', new_owner_chat_id: '1.2', star_count: '0' }, '')).toThrow()
  })
  it('keeps zero-cost gift operations exact and bounds paid transfers', () => {
    expect(buildActionParams(spec('upgradeGift'), { owned_gift_id: 'gift', star_count: '0' }, '')).toEqual({ owned_gift_id: 'gift', star_count: 0, keep_original_details: false })
    for (const star_count of ['-1', '10001', '1e2', '']) expect(() => buildActionParams(spec('transferBusinessAccountStars'), { star_count }, '')).toThrow()
  })
  it('rejects duplicate, invalid or oversized message deletion targets', () => {
    for (const message_ids of ['1,1', '0', '2147483648', '1a', Array.from({ length: 101 }, (_, i) => i + 1).join(',')]) expect(() => buildActionParams(spec('delete_all_business_messages'), { message_ids }, '')).toThrow()
    expect(buildActionParams(spec('delete_sent_business_messages'), { message_ids: '1, 2\n3' }, '')).toEqual({ message_ids: [1,2,3] })
  })
  it('uses only a scoped uploaded reference for photo inputs, without URL fetches', () => {
    expect(() => buildActionParams(spec('setBusinessAccountProfilePhoto'), {}, 'https://example.com/photo')).toThrow()
    expect(buildActionParams(spec('setBusinessAccountProfilePhoto'), {}, 'bmedia_safe')).toEqual({ photo: { type: 'static', photo: 'bmedia_safe' }, is_public: false })
    expect(buildActionParams(spec('postStory'), { active_period: '21600' }, 'bmedia_safe')).toEqual({ content: { type: 'photo', photo: 'bmedia_safe' }, active_period: 21600, caption: '', post_to_chat_page: false, protect_content: false })
  })
  it('preserves deliberate empty profile text and rejects invalid usernames', () => {
    expect(buildActionParams(spec('setBusinessAccountBio'), { bio: '' }, '')).toEqual({ bio: '' })
    expect(() => buildActionParams(spec('setBusinessAccountUsername'), { username: '@other' }, '')).toThrow()
  })
})
