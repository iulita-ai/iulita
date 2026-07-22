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

  describe('Slack posting config', () => {
    it('defaults write_mode to off and empty channels', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: '{}' },
      })
      const vm = wrapper.vm as any
      expect(vm.slackCfg.write_mode).toBe('off')
      expect(vm.slackCfg.write_channels).toEqual([])
      expect(vm.slackCfg.max_posts_per_hour).toBe(0)
    })

    it('parses existing posting config', () => {
      const cfg = { write_mode: 'auto', write_channels: ['C1', 'C2'], guardrails: { max_posts_per_hour: 5, quiet_hours: [22, 7] } }
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: JSON.stringify(cfg) },
      })
      const vm = wrapper.vm as any
      expect(vm.slackCfg.write_mode).toBe('auto')
      expect(vm.slackCfg.write_channels).toEqual(['C1', 'C2'])
      expect(vm.slackCfg.max_posts_per_hour).toBe(5)
      expect(vm.slackCfg.quiet_start).toBe(22)
      expect(vm.slackCfg.quiet_end).toBe(7)
    })

    it('emits write_channels change', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: '{}' },
      })
      const vm = wrapper.vm as any
      vm.onWriteChannelsChange(['C123'])
      const emitted = wrapper.emitted('update:modelValue')
      expect(JSON.parse(emitted![emitted!.length - 1][0] as string).write_channels).toEqual(['C123'])
    })

    it('commits non-auto write_mode directly', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: '{}' },
      })
      const vm = wrapper.vm as any
      vm.onWriteModeChange('draft')
      const emitted = wrapper.emitted('update:modelValue')
      expect(JSON.parse(emitted![emitted!.length - 1][0] as string).write_mode).toBe('draft')
    })

    it('commitAuto seeds a default rate cap and quiet hours', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: '{}' },
      })
      const vm = wrapper.vm as any
      vm.commitAuto()
      const emitted = wrapper.emitted('update:modelValue')
      const cfg = JSON.parse(emitted![emitted!.length - 1][0] as string)
      expect(cfg.write_mode).toBe('auto')
      expect(cfg.guardrails.max_posts_per_hour).toBe(1)
      expect(cfg.guardrails.quiet_hours).toEqual([0, 0])
    })

    it('commitAuto preserves an existing positive rate cap', () => {
      const start = { guardrails: { max_posts_per_hour: 5, quiet_hours: [22, 7] } }
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: JSON.stringify(start) },
      })
      const vm = wrapper.vm as any
      vm.commitAuto()
      const emitted = wrapper.emitted('update:modelValue')
      const cfg = JSON.parse(emitted![emitted!.length - 1][0] as string)
      expect(cfg.write_mode).toBe('auto')
      expect(cfg.guardrails.max_posts_per_hour).toBe(5)
      expect(cfg.guardrails.quiet_hours).toEqual([22, 7])
    })

    it('emits guardrail updates', () => {
      const wrapper = mount(ChannelConfigForm, {
        props: { channelType: 'slack', modelValue: '{}' },
      })
      const vm = wrapper.vm as any
      vm.updateGuardrail('max_posts_per_hour', 3)
      const emitted = wrapper.emitted('update:modelValue')
      const g = JSON.parse(emitted![emitted!.length - 1][0] as string).guardrails
      expect(g.max_posts_per_hour).toBe(3)
      expect(g.quiet_hours).toEqual([0, 0])
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
