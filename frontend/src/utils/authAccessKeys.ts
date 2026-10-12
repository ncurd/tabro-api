export function authAccessKeysUrl(accountCenterUrl: string | undefined): string | null {
  if (!accountCenterUrl) return null
  try {
    const url = new URL(accountCenterUrl)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return null
    const basePath = url.pathname.replace(/\/Identity\/Account\/Manage(?:\/.*)?$/i, '').replace(/\/+$/, '')
    url.pathname = `${basePath}/Identity/Account/Manage/AccessKeys`
    url.search = ''
    url.hash = ''
    return url.toString()
  } catch {
    return null
  }
}
