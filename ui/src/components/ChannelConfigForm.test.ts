import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ChannelConfigForm from './ChannelConfigForm.vue'

describe('ChannelConfigForm.vue', () => {
  describe('Telegram config', () => {
    it('renders telegram fields', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: JSON.stringify({ token: 'test-token' }),
        },
      })

      expect(wrapper.text()).toContain('Bot Token')
      expect(wrapper.text()).toContain('Allowed User IDs')
      expect(wrapper.text()).toContain('Debounce Window')
      expect(wrapper.text()).toContain('Rate Limit')
      expect(wrapper.text()).toContain('Rate Window')
    })

    it('parses telegram config from JSON', () => {
      const config = { token: 't', allowed_ids: [123, 456], debounce_window: '2s', rate_limit: 10, rate_window: '1m' }
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: JSON.stringify(config),
        },
      })

      const vm = wrapper.vm as any
      expect(vm.tg.token).toBe('t')
      expect(vm.tg.allowed_ids).toEqual([123, 456])
      expect(vm.tg.debounce_window).toBe('2s')
      expect(vm.tg.rate_limit).toBe(10)
    })

    it('uses defaults for missing telegram fields', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: '{}',
        },
      })

      const vm = wrapper.vm as any
      expect(vm.tg.token).toBe('')
      expect(vm.tg.allowed_ids).toEqual([])
      expect(vm.tg.debounce_window).toBe('1.5s')
      expect(vm.tg.rate_limit).toBe(0)
      expect(vm.tg.rate_window).toBe('1m')
    })

    it('handles empty modelValue', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: '',
        },
      })

      const vm = wrapper.vm as any
      expect(vm.tg.token).toBe('')
    })

    it('handles invalid JSON gracefully', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: 'not json',
        },
      })

      const vm = wrapper.vm as any
      expect(vm.tg.token).toBe('')
    })

    it('emits update on field change', async () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: '{}',
        },
      })

      const vm = wrapper.vm as any
      vm.update('token', 'new-token')

      const emitted = wrapper.emitted('update:modelValue')
      expect(emitted).toBeTruthy()
      const parsed = JSON.parse(emitted![0][0] as string)
      expect(parsed.token).toBe('new-token')
    })

    it('filters invalid allowed IDs', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: '{}',
        },
      })

      const vm = wrapper.vm as any
      vm.onAllowedIdsChange(['123', 'abc', '456', '-1', '0'])

      const emitted = wrapper.emitted('update:modelValue')
      const parsed = JSON.parse(emitted![0][0] as string)
      expect(parsed.allowed_ids).toEqual([123, 456])
    })
  })

  describe('Discord config', () => {
    it('renders discord fields', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'discord',
          modelValue: '{}',
        },
      })

      expect(wrapper.text()).toContain('Bot Token')
      expect(wrapper.text()).toContain('Allowed Channel IDs')
    })

    it('parses discord config', () => {
      const config = { token: 'disc-token', allowed_channel_ids: ['ch1', 'ch2'] }
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'discord',
          modelValue: JSON.stringify(config),
        },
      })

      const vm = wrapper.vm as any
      expect(vm.discord.token).toBe('disc-token')
      expect(vm.discord.allowed_channel_ids).toEqual(['ch1', 'ch2'])
    })
  })

  describe('Web config', () => {
    it('shows no configuration message', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'web',
          modelValue: '{}',
        },
      })

      expect(wrapper.text()).toContain('No configuration required')
    })
  })

  describe('Unknown channel type', () => {
    it('renders raw JSON textarea', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'unknown',
          modelValue: '{"custom": true}',
        },
      })

      expect(wrapper.text()).toContain('Config (JSON)')
    })

    it('emits raw JSON on change', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'unknown',
          modelValue: '{}',
        },
      })

      const vm = wrapper.vm as any
      vm.onRawJsonChange('{"new": true}')

      const emitted = wrapper.emitted('update:modelValue')
      expect(emitted![0][0]).toBe('{"new": true}')
    })
  })

  describe('disabled state', () => {
    it('passes disabled prop', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: {
          channelType: 'telegram',
          modelValue: '{}',
          disabled: true,
        },
      })

      // Check that the disabled prop is propagated
      expect(wrapper.props().disabled).toBe(true)
    })
  })
})
