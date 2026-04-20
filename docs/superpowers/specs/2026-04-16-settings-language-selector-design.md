# Settings Language Selector Design

## Summary

Add a language selector dropdown to the admin settings page so the admin can switch the current frontend interface language from within settings. Reuse the existing frontend i18n system and local persistence. Do not add any backend setting or site-wide default language behavior.

This work also formalizes the existing legacy locale migration from `zh` to `zh-CN` and removes remaining code paths that still compare the locale to `zh` directly.

## Goals

- Add a visible language selector dropdown in the general settings area of the admin settings page.
- Reuse the existing supported locale list that has already been added to the frontend.
- Apply the locale change immediately without requiring a full page refresh.
- Persist the selected locale using the existing frontend `localStorage` key.
- Normalize legacy `zh` locale values to `zh-CN`.
- Replace remaining direct `locale === 'zh'` checks with normalized Chinese-locale checks.

## Non-Goals

- No backend API or database changes.
- No site-wide default language setting.
- No server-side per-user locale preference.
- No removal of the existing header language switcher.
- No changes to the locale message files beyond what is already present in the worktree.

## Current State

- Frontend i18n is centralized in `frontend/src/i18n/index.ts`.
- Supported locales are already defined in `availableLocales`.
- Locale persistence already uses the `sub2api_locale` local storage key.
- Legacy locale normalization already maps `zh` to `zh-CN`.
- A header-level `LocaleSwitcher` already exists.
- The admin settings page does not currently expose a language dropdown.
- `frontend/src/views/admin/SettingsView.vue` still contains locale checks against `zh` for payment documentation links.

## Source Of Truth

The existing i18n module remains the single source of truth for locale values:

- `availableLocales` defines the list of selectable languages.
- `setLocale()` performs lazy loading, updates `i18n.global.locale`, updates `localStorage`, and refreshes the document title.
- `LEGACY_LOCALE_MAP` continues to normalize `zh` to `zh-CN`.

No new locale registry or settings state will be introduced in the settings page.

## Settings Page UI

Add a new field in the General / Site Settings section of `frontend/src/views/admin/SettingsView.vue`.

Behavior:

- The field label explains that it controls the current browser’s interface language.
- The dropdown options are derived from `availableLocales`.
- The current selected value is bound to the active i18n locale.
- Changing the selection calls `setLocale()` immediately.
- The setting is not part of the admin save payload and does not depend on clicking the page-level save button.
- The hint text explicitly states that the preference is stored locally in the current browser.

Implementation choice:

- Use a simple native `<select>` styled with the existing `input` class, because the option set is small and no search or custom rendering is required.
- Keep the logic local to `SettingsView.vue` rather than introducing a new shared component for this small feature.

## Locale Normalization

Legacy `zh` handling remains supported through the i18n normalization path. The implementation should ensure:

- Existing users with `sub2api_locale=zh` are transparently migrated to `zh-CN`.
- New UI code does not compare against `zh` directly.
- Chinese-specific behavior uses normalized checks such as `startsWith('zh')` where appropriate.

## Documentation Link Behavior

The payment documentation links in `SettingsView.vue` currently branch on `locale === 'zh'`. This should be replaced with a normalized Chinese-locale check so that:

- `zh-CN` uses the Chinese payment document.
- `zh-TW` also counts as a Chinese locale for the Chinese documentation path unless a separate Traditional Chinese payment document exists later.
- All non-Chinese locales continue to use the English payment document.

## Error Handling

Locale switching is expected to succeed under normal conditions, but the settings page should still handle failures defensively:

- If `setLocale()` throws, keep the previous locale value.
- Show a toast error message using existing app toast infrastructure.
- Do not affect unrelated admin settings state.

## Testing

Tests should cover the new behavior before production code is added:

1. i18n normalization test:
   Verify that a stored legacy `zh` locale is normalized to `zh-CN`.
2. settings page language switch test:
   Verify that changing the new dropdown calls `setLocale()` with the selected locale.
3. Chinese docs link test:
   Verify that normalized Chinese locales resolve to the Chinese payment documentation link.

If the settings page test is too expensive to introduce immediately, the fallback is:

- a focused unit test around the locale normalization path, and
- a focused view test for the docs-link locale branch.

## Files Expected To Change

- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/i18n/index.ts` only if a small normalization or export adjustment is required during implementation
- one or more new or existing frontend tests under `frontend/src/i18n/__tests__` or `frontend/src/views/admin/__tests__`

## Open Questions Resolved

- The dropdown is local-only, not a backend-backed admin setting.
- The existing header switcher remains in place.
- `zh` is treated as a legacy alias and normalized to `zh-CN`.
