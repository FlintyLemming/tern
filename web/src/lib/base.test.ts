import { appPath, readBase, routerBase, setBase, url } from './base'

function docWith(content?: string): Document {
  const d = document.implementation.createHTMLDocument('t')
  if (content !== undefined) {
    const m = d.createElement('meta')
    m.name = 'tern-base'
    m.content = content
    d.head.appendChild(m)
  }
  return d
}

it('reads the base the server declares', () => {
  expect(readBase(docWith())).toBe('/')
  expect(readBase(docWith('/tern/'))).toBe('/tern/')
  expect(readBase(docWith('/apps/tern-2/'))).toBe('/apps/tern-2/')
  for (const bad of ['', 'tern/', '/tern', '//evil.example/', 'https://x/', '/a b/', '/%2e/']) {
    expect(readBase(docWith(bad)), bad).toBe('/')
  }
})

it('puts paths below the base', () => {
  expect(url('/api/v1/status')).toBe('/api/v1/status')
  expect(routerBase()).toBe('')
  expect(appPath('/tasks/a')).toBe('/tasks/a')

  setBase('/tern/')
  expect(url('/api/v1/status')).toBe('/tern/api/v1/status')
  expect(routerBase()).toBe('/tern')
  expect(appPath('/tern/tasks/a')).toBe('/tasks/a')
  expect(appPath('/tern/')).toBe('/')
  expect(appPath('/tern')).toBe('/')
  expect(appPath('/ternx/a')).toBe('/ternx/a')

  setBase('nonsense')
  expect(url('/x')).toBe('/x')
})
