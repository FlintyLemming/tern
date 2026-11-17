import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach } from 'vitest'
import i18n, { applyPlatform } from '../i18n'
import { setBase } from '../lib/base'

// Radix primitives use pointer capture and scrollIntoView, which jsdom lacks.
Element.prototype.hasPointerCapture ??= () => false
Element.prototype.releasePointerCapture ??= () => {}
Element.prototype.setPointerCapture ??= () => {}
Element.prototype.scrollIntoView ??= () => {}
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(async () => {
  await i18n.changeLanguage('zh') // tests assert the Chinese copy unless they switch
})

afterEach(() => {
  cleanup()
  localStorage.clear()
  applyPlatform('docker') // tests on the QNAP wording switch back
  setBase('/')
})
