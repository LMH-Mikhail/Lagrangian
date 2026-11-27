import DOMPurify from 'dompurify'
import { marked } from 'marked'
import type { KnowledgeDoc } from '../types/models'

export interface FrontmatterField {
  key: string
  value: string | string[]
}

export interface TocItem {
  id: string
  level: number
  text: string
}

export interface RenderedMarkdown {
  frontmatter: FrontmatterField[]
  html: string
  toc: TocItem[]
}

function stripExt(p: string): string {
  return p.replace(/\.(markdown|md|txt)$/i, '')
}

function baseName(p: string): string {
  const idx = p.lastIndexOf('/')
  return idx >= 0 ? p.slice(idx + 1) : p
}

function dirName(p: string): string {
  const idx = p.lastIndexOf('/')
  return idx >= 0 ? p.slice(0, idx) : ''
}

function normalizePosix(p: string): string {
  const parts = p.split('/')
  const stack: string[] = []
  for (const part of parts) {
    if (!part || part === '.') continue
    if (part === '..') {
      stack.pop()
    } else {
      stack.push(part)
    }
  }
  return stack.join('/')
}

function topGroup(p: string): string {
  const idx = p.indexOf('/')
  return idx > 0 ? p.slice(0, idx) : ''
}

export function parseFrontmatter(raw: string): {
  fields: FrontmatterField[]
  body: string
} {
  const normalized = raw.replace(/\r\n/g, '\n')
  if (!normalized.startsWith('---\n')) {
    return { fields: [], body: normalized }
  }
  const endIdx = normalized.indexOf('\n---', 4)
  if (endIdx < 0) {
    return { fields: [], body: normalized }
  }
  const afterClose = normalized.slice(endIdx + 4)
  if (afterClose.length > 0 && afterClose[0] !== '\n') {
    return { fields: [], body: normalized }
  }

  const yamlBlock = normalized.slice(4, endIdx)
  const body = afterClose.startsWith('\n') ? afterClose.slice(1) : afterClose
  const fields: FrontmatterField[] = []
  const lines = yamlBlock.split('\n')

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line.trim() || line.trim().startsWith('#')) continue
    const m = line.match(/^([A-Za-z0-9_\-\u4e00-\u9fff]+)\s*:\s*(.*)$/)
    if (!m) continue
    const key = m[1].trim()
    const rest = m[2].trim()

    if (rest.startsWith('[') && rest.endsWith(']')) {
      const inner = rest.slice(1, -1).trim()
      const arr = inner
        ? inner
            .split(',')
            .map((s) => s.trim().replace(/^["']|["']$/g, ''))
            .filter(Boolean)
        : []
      fields.push({ key, value: arr })
      continue
    }

    if (rest === '') {
      const items: string[] = []
      while (i + 1 < lines.length && /^\s*-\s+/.test(lines[i + 1])) {
        i++
        items.push(lines[i].replace(/^\s*-\s+/, '').trim().replace(/^["']|["']$/g, ''))
      }
      if (items.length > 0) {
        fields.push({ key, value: items })
      } else {
        fields.push({ key, value: '' })
      }
      continue
    }

    fields.push({ key, value: rest.replace(/^["']|["']$/g, '') })
  }

  return { fields, body }
}

export function resolveWikiTarget(
  rawTarget: string,
  currentRelPath: string,
  docs: KnowledgeDoc[],
): { relPath: string | null; heading: string } {
  let t = rawTarget.trim()
  let heading = ''
  const hashIdx = t.indexOf('#')
  if (hashIdx >= 0) {
    heading = t.slice(hashIdx + 1).trim()
    t = t.slice(0, hashIdx).trim()
  }
  if (!t) {
    return { relPath: currentRelPath || null, heading }
  }
  t = t.replace(/^\.\//, '').replace(/^\//, '')

  const exactMap = new Map<string, string>()
  const noExtMap = new Map<string, string>()
  const stemMap = new Map<string, string[]>()

  for (const d of docs) {
    const low = d.rel_path.toLowerCase()
    exactMap.set(low, d.rel_path)
    noExtMap.set(stripExt(low), d.rel_path)
    const st = stripExt(baseName(low))
    const list = stemMap.get(st) || []
    list.push(d.rel_path)
    stemMap.set(st, list)
  }

  const lowT = t.toLowerCase()
  const lowNoExt = stripExt(lowT)
  if (exactMap.has(lowT)) return { relPath: exactMap.get(lowT)!, heading }
  if (noExtMap.has(lowNoExt)) return { relPath: noExtMap.get(lowNoExt)!, heading }

  const curDir = dirName(currentRelPath)
  if (curDir) {
    const joined = normalizePosix(`${curDir}/${t}`).toLowerCase()
    if (exactMap.has(joined)) return { relPath: exactMap.get(joined)!, heading }
    if (noExtMap.has(stripExt(joined))) return { relPath: noExtMap.get(stripExt(joined))!, heading }
  }

  const stem = stripExt(baseName(lowT))
  const candidates = stemMap.get(stem)
  if (candidates && candidates.length > 0) {
    if (candidates.length === 1) return { relPath: candidates[0], heading }
    const srcGrp = topGroup(currentRelPath)
    let best = candidates[0]
    for (const cand of candidates) {
      if (topGroup(cand) === srcGrp && topGroup(best) !== srcGrp) {
        best = cand
        continue
      }
      if (topGroup(cand) === topGroup(best) && cand.length < best.length) {
        best = cand
      }
    }
    return { relPath: best, heading }
  }

  return { relPath: null, heading }
}

function escapeAttr(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// Transform [[wikilinks]] outside fenced code blocks and inline code spans.
function replaceWikiLinks(body: string, currentRelPath: string, docs: KnowledgeDoc[]): string {
  const lines = body.split('\n')
  let inFence = false
  const out = lines.map((line) => {
    if (/^\s*```/.test(line)) {
      inFence = !inFence
      return line
    }
    if (inFence) return line

    // Split by inline code spans so `[[literal]]` stays untouched
    const segments = line.split(/(`[^`]+`)/g)
    return segments
      .map((seg, idx) => {
        if (idx % 2 === 1) return seg
        return seg.replace(/\[\[([^\[\]\r\n]+)\]\]/g, (_full, inner: string) => {
          let targetPart = inner
          let label = ''
          const escPipe = inner.indexOf('\\|')
          const rawPipe = inner.indexOf('|')
          if (escPipe >= 0) {
            targetPart = inner.slice(0, escPipe).trim()
            label = inner.slice(escPipe + 2).trim()
          } else if (rawPipe >= 0) {
            targetPart = inner.slice(0, rawPipe).trim()
            label = inner.slice(rawPipe + 1).trim()
          }
          const resolved = resolveWikiTarget(targetPart, currentRelPath, docs)
          const display = label || stripExt(baseName(targetPart.split('#')[0] || targetPart))
          const cls = resolved.relPath ? 'wiki-link' : 'wiki-link unresolved'
          const targetAttr = resolved.relPath
            ? `${resolved.relPath}${resolved.heading ? `#${resolved.heading}` : ''}`
            : targetPart
          return `<a href="#" class="${cls}" data-wiki-target="${escapeAttr(targetAttr)}" data-wiki-resolved="${resolved.relPath ? '1' : '0'}">${escapeHtml(display)}</a>`
        })
      })
      .join('')
  })
  return out.join('\n')
}

export function renderKnowledgeMarkdown(
  raw: string,
  currentRelPath: string,
  docs: KnowledgeDoc[],
): RenderedMarkdown {
  const { fields, body } = parseFrontmatter(raw)
  const withWiki = replaceWikiLinks(body, currentRelPath, docs)
  const rawHtml = marked.parse(withWiki, { gfm: true, breaks: false, async: false }) as string
  const safeHtml = DOMPurify.sanitize(rawHtml, {
    ADD_ATTR: ['data-wiki-target', 'data-wiki-resolved', 'target', 'rel'],
  })

  // Assign deterministic heading IDs and extract TOC from sanitized HTML
  const toc: TocItem[] = []
  const parser = new DOMParser()
  const dom = parser.parseFromString(`<div>${safeHtml}</div>`, 'text/html')
  const container = dom.body.firstElementChild as HTMLElement | null
  if (container) {
    const headings = container.querySelectorAll('h1, h2, h3, h4')
    headings.forEach((h, idx) => {
      const id = `md-h-${idx}`
      h.setAttribute('id', id)
      const level = Number(h.tagName.slice(1))
      const text = (h.textContent || '').trim()
      if (text) {
        toc.push({ id, level, text })
      }
    })
    // Ensure external links open cleanly in new tab
    container.querySelectorAll('a').forEach((a) => {
      const href = a.getAttribute('href') || ''
      if (/^https?:\/\//i.test(href)) {
        a.setAttribute('target', '_blank')
        a.setAttribute('rel', 'noopener noreferrer')
      }
    })
    return {
      frontmatter: fields,
      html: container.innerHTML,
      toc,
    }
  }

  return { frontmatter: fields, html: safeHtml, toc }
}
