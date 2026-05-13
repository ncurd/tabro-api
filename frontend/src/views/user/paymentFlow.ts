import type { OrderStatus } from '@/types/payment'
import { isStripePaymentType } from '@/utils/paymentCurrency'

const SUCCESS_STATUSES = new Set<OrderStatus>(['COMPLETED', 'PAID', 'RECHARGING'])

export type PaymentPageOpenStrategy = 'same-tab' | 'new-tab' | 'popup'

export interface PaymentPageWindow {
  closed?: boolean
  opener?: unknown
  focus?: () => void
  close?: () => void
  location: {
    href: string
  }
  document?: {
    write?: (html: string) => void
    close?: () => void
    body?: {
      innerHTML: string
    } | null
  }
}

export type PaymentWindowOpener = (url?: string, target?: string, features?: string) => PaymentPageWindow | null

export function resolvePaymentPageOpenStrategy(paymentType: string, isMobile: boolean): PaymentPageOpenStrategy {
  if (isStripePaymentType(paymentType)) return 'new-tab'
  if (isMobile) return 'same-tab'
  return 'popup'
}

export function shouldTrackPaymentInline(strategy: PaymentPageOpenStrategy): boolean {
  return strategy !== 'new-tab'
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

export function openPendingPaymentPage(
  openWindow: PaymentWindowOpener,
  title: string,
  message: string,
): PaymentPageWindow | null {
  const pendingTab = openWindow('', '_blank')
  if (!pendingTab || pendingTab.closed) {
    return null
  }

  try {
    pendingTab.opener = null
  } catch {
    // Ignore opener assignment failures in restricted browsers.
  }

  const safeTitle = escapeHtml(title)
  const safeMessage = escapeHtml(message)
  const placeholderHtml = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>${safeTitle}</title>
    <style>
      :root { color-scheme: light; }
      body {
        margin: 0;
        min-height: 100vh;
        display: grid;
        place-items: center;
        background: #f5f7fb;
        color: #111827;
        font: 16px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      }
      .panel {
        width: min(420px, calc(100vw - 32px));
        padding: 24px;
        border-radius: 20px;
        background: #ffffff;
        box-shadow: 0 18px 48px rgba(15, 23, 42, 0.12);
      }
      .spinner {
        width: 28px;
        height: 28px;
        border: 3px solid #dbeafe;
        border-top-color: #2563eb;
        border-radius: 999px;
        animation: spin 0.8s linear infinite;
      }
      p {
        margin: 16px 0 0;
      }
      @keyframes spin {
        to { transform: rotate(360deg); }
      }
    </style>
  </head>
  <body>
    <div class="panel">
      <div class="spinner"></div>
      <p>${safeMessage}</p>
    </div>
  </body>
</html>`

  try {
    if (typeof pendingTab.document?.write === 'function') {
      pendingTab.document.write(placeholderHtml)
      pendingTab.document.close?.()
    } else if (pendingTab.document?.body) {
      pendingTab.document.body.innerHTML = placeholderHtml
    }
    pendingTab.focus?.()
  } catch {
    // Ignore document access failures; navigation can still proceed later.
  }

  return pendingTab
}

export function navigatePendingPaymentPage(
  pendingTab: PaymentPageWindow | null,
  url: string,
  openWindow: PaymentWindowOpener,
): boolean {
  if (pendingTab && !pendingTab.closed) {
    try {
      pendingTab.location.href = url
      pendingTab.focus?.()
      return true
    } catch {
      // Fall through to opening a fresh tab.
    }
  }

  const fallbackTab = openWindow(url, '_blank')
  if (!fallbackTab || fallbackTab.closed) {
    return false
  }

  try {
    fallbackTab.opener = null
    fallbackTab.focus?.()
  } catch {
    // Ignore restricted browser behavior.
  }
  return true
}

export function closePendingPaymentPage(pendingTab: PaymentPageWindow | null): void {
  if (!pendingTab || pendingTab.closed) {
    return
  }
  try {
    pendingTab.close?.()
  } catch {
    // Ignore close failures.
  }
}

export function isPaymentResultSuccessful(
  orderStatus?: OrderStatus | null,
  queryStatus?: string | null,
  tradeStatus?: string | null,
): boolean {
  if (orderStatus) {
    return SUCCESS_STATUSES.has(orderStatus)
  }
  return queryStatus === 'success' || tradeStatus === 'TRADE_SUCCESS'
}
