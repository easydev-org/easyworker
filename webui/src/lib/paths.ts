// Cross-platform path helpers for the control panel.
//
// The worker reports paths in SLASH form (it converts Windows backslashes), so
// everything here is slash-based. Two anchors matter:
//   - the WORKSPACE (the app's logical root, shown as `~`)
//   - the filesystem ROOT (unix `/`, Windows a drive like `C:`)
// `~` always resolves to the WORKSPACE (the app root), never the OS home.

export interface Anchors {
  /** Absolute workspace root, slash form (e.g. /root/workspace or C:/app). */
  workspace: string
}

let anchors: Anchors = { workspace: '' }

export function setAnchors(a: Anchors) {
  anchors = a
}

/** Split a leading Windows drive (`C:`) from the rest of a slash path. */
function splitDrive(p: string): [string, string] {
  const m = /^([A-Za-z]:)(\/.*)?$/.exec(p)
  if (m) return [m[1]!.toUpperCase(), m[2] ?? '/']
  return ['', p]
}

/** Collapse //, ., .. in an absolute path, preserving a drive prefix. `..` at
 *  the root stays at the root. Backslashes are accepted and normalized. */
export function normAbs(p: string): string {
  const [drive, rest] = splitDrive(String(p).replace(/\\/g, '/'))
  const parts: string[] = []
  for (const seg of rest.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length) parts.pop()
      continue
    }
    parts.push(seg)
  }
  return `${drive}/${parts.join('/')}`
}

/** The filesystem root of `abs` (`/` on unix, `C:/` on Windows). */
export function rootOf(abs: string): string {
  const [drive] = splitDrive(abs)
  return `${drive}/`
}

/** Resolve `target` (may be relative, `~`, or absolute) against `base`. */
export function resolveAbs(
  base: string,
  target: string | null | undefined,
): string {
  const t = String(target ?? '').trim()
  if (t === '' || t === '~') return anchors.workspace || '/'
  if (t === '~/') return anchors.workspace || '/'
  if (t.startsWith('~/')) return normAbs(`${anchors.workspace}/${t.slice(2)}`)
  // Windows drive-absolute (C:\ or C:/).
  if (/^[A-Za-z]:/.test(t)) return normAbs(t)
  if (t.startsWith('/')) return normAbs(rootOf(anchors.workspace || '/') + t)
  return normAbs(`${base || '/'}/${t}`)
}

/** Make a FileList entry path absolute. Entries are workspace-relative when
 *  inside the root and absolute (with a leading `/` or a drive) outside. */
export function absOf(p: string): string {
  const s = String(p)
  if (s.startsWith('/') || /^[A-Za-z]:/.test(s)) return normAbs(s)
  return normAbs(`${anchors.workspace}/${s}`)
}

/** Human display: the workspace shows as `~`; paths under it as `~/sub`. */
export function displayPath(abs: string): string {
  const ws = anchors.workspace
  if (!ws) return abs
  if (abs === ws) return '~'
  if (abs.startsWith(ws + '/')) return `~/${abs.slice(ws.length + 1)}`
  return abs
}

export function basename(p: string): string {
  return String(p).replace(/\/+$/, '').split('/').pop() || p
}

/** Breadcrumb segments for an absolute dir. When inside the workspace the
 *  first crumb is `~`; otherwise it is the filesystem root (`/` or `C:/`). */
export function crumbsOf(abs: string): { name: string; path: string }[] {
  const ws = anchors.workspace
  if (ws && (abs === ws || abs.startsWith(ws + '/'))) {
    const out = [{ name: '~', path: ws }]
    let acc = ws
    for (const seg of abs.slice(ws.length).split('/').filter(Boolean)) {
      acc += `/${seg}`
      out.push({ name: seg, path: acc })
    }
    return out
  }
  const root = rootOf(abs)
  const out = [
    { name: root === '/' ? '/' : root.replace(/\/$/, ''), path: root },
  ]
  let acc = root.replace(/\/$/, '')
  for (const seg of abs.slice(root.length).split('/').filter(Boolean)) {
    acc += `/${seg}`
    out.push({ name: seg, path: acc })
  }
  return out
}
