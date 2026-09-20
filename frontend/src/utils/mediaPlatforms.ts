import type { AccountPlatform } from '@/types'

export const mediaPlatformOptions: { value: AccountPlatform; label: string }[] = [
  { value: 'dashscope', label: 'DashScope / Wan' },
  { value: 'volcengine_ark', label: 'Volcengine Ark / Seedance' },
  { value: 'azure_speech', label: 'Azure Speech' }
]

export function isMediaPlatform(platform: string): boolean {
  return mediaPlatformOptions.some(option => option.value === platform)
}

export function accountDefaultBaseURL(platform: string): string {
  switch (platform) {
    case 'openai': return 'https://api.openai.com'
    case 'gemini': return 'https://generativelanguage.googleapis.com'
    case 'antigravity': return 'https://cloudcode-pa.googleapis.com'
    case 'dashscope': return 'https://dashscope.aliyuncs.com'
    case 'volcengine_ark': return 'https://ark.cn-beijing.volces.com/api/v3'
    case 'azure_speech': return ''
    default: return 'https://api.anthropic.com'
  }
}
