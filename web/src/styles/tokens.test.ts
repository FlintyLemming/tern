import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')

// The front matter block "name:" of DESIGN-notion.md as key → value.
function frontMatterBlock(md: string, name: string): Record<string, string> {
  const fm = md.split(/^---$/m)[1]
  const lines = fm.split('\n')
  const start = lines.findIndex((l) => l === `${name}:`)
  if (start < 0) throw new Error(`no ${name} block`)
  const out: Record<string, string> = {}
  for (const line of lines.slice(start + 1)) {
    const m = /^ {2}([a-z-]+): "?([^"]+)"?$/.exec(line)
    if (!m) break
    out[m[1]] = m[2]
  }
  return out
}

function cssVars(css: string, prefix: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const m of css.matchAll(new RegExp(`--${prefix}-([a-z0-9-]+):\\s*([^;]+);`, 'g'))) {
    out[m[1]] = m[2].trim()
  }
  return out
}

const design = read('../../../DESIGN-notion.md')
const tokens = read('./tokens.css')

const designColors = [
  'primary', 'primary-active', 'on-primary', 'canvas', 'canvas-soft', 'surface', 'ink', 'ink-secondary',
  'ink-muted', 'ink-faint', 'hairline', 'accent-sky', 'accent-orange', 'accent-green',
]

it('matches DESIGN-notion.md colors', () => {
  const want = frontMatterBlock(design, 'colors')
  const got = cssVars(tokens, 'color')
  for (const name of designColors) {
    expect(want[name], name).toBeDefined()
    expect(got[name]?.toLowerCase(), name).toBe(want[name].toLowerCase())
  }
})

it('matches DESIGN-notion.md radii', () => {
  const want = frontMatterBlock(design, 'rounded')
  const got = cssVars(tokens, 'radius')
  for (const name of ['xs', 'sm', 'md', 'lg', 'xl', 'full']) {
    expect(got[name], name).toBe(want[name])
  }
})

it('adds exactly one color: danger', () => {
  const got = Object.keys(cssVars(tokens, 'color')).sort()
  expect(got).toEqual([...designColors, 'input-border', 'danger'].sort())
  expect(cssVars(tokens, 'color').danger.toLowerCase()).toBe('#d44c47')
})
