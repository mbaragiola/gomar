import { ref } from 'vue'

const key = 'gomar-session-revocation'
let memoryOnly = false

function storedAttempt() {
  try {
    const value = localStorage.getItem(key)
    if (value) return value
  } catch { /* Use the tab's storage if origin storage is unavailable. */ }
  try { return sessionStorage.getItem(key) } catch { return null }
}

export const pendingSignOut = ref(storedAttempt())

export function syncPendingSignOut(event: StorageEvent) {
  if (event.key === key && event.newValue) {
    // A suspended tab can receive the notification after revocation completed.
    pendingSignOut.value = event.newValue
  }
  if (event.key === key || event.key === null) pendingSignOut.value = storedAttempt()
}

export function beginSignOut() {
  const attempt = storedAttempt() ?? crypto.randomUUID()
  try { localStorage.setItem(key, attempt) } catch {
    try { sessionStorage.setItem(key, attempt) } catch { memoryOnly = true }
  }
  pendingSignOut.value = attempt
}

export function isPendingSignOut(attempt: string) {
  return pendingSignOut.value === attempt && (memoryOnly || storedAttempt() === attempt)
}

export function completeSignOut(attempt: string) {
  if (!isPendingSignOut(attempt)) return
  try { if (localStorage.getItem(key) === attempt) localStorage.removeItem(key) } catch { /* Storage may be unavailable. */ }
  try { if (sessionStorage.getItem(key) === attempt) sessionStorage.removeItem(key) } catch { /* Storage may be unavailable. */ }
  memoryOnly = false
  pendingSignOut.value = storedAttempt()
}

export async function withSignOutLock(revoke: () => Promise<void>) {
  if (navigator.locks) {
    await navigator.locks.request(key, revoke)
  } else {
    await revoke()
  }
}
