import { cleanPath, parentPath } from './paths'

it('cleans typed paths', () => {
  expect(cleanPath('/a//b/./c/')).toBe('/a/b/c')
  expect(cleanPath('/a/../b')).toBe('/b')
  expect(cleanPath('/..')).toBe('/')
  expect(cleanPath('/')).toBe('/')
  expect(cleanPath('a/b/')).toBe('a/b')
  expect(cleanPath('./a')).toBe('a')
  expect(cleanPath('../a')).toBe('../a')
  expect(cleanPath('')).toBe('')
  expect(cleanPath('.')).toBe('')
})

it('finds the parent folder', () => {
  expect(parentPath('/a/b')).toBe('/a')
  expect(parentPath('/a')).toBe('/')
  expect(parentPath('/')).toBeNull()
  expect(parentPath('a/b')).toBe('a')
  expect(parentPath('a')).toBe('')
  expect(parentPath('')).toBeNull()
})
