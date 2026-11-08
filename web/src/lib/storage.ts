// Per-browser preferences in localStorage. Storage can be blocked (private
// windows, site data turned off); then nothing is remembered and reads return null.

export function loadSetting(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

export function saveSetting(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    // storage blocked: the setting just isn't remembered
  }
}
