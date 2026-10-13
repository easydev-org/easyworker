// Extension-based file classification for the Files preview. The worker's
// FileList/FileRead do not carry a MIME type, so we infer from the name — good
// enough for the three preview kinds we support (image / audio / video) and for
// deciding "text (editable) vs binary".

export type PreviewKind = 'image' | 'audio' | 'video' | 'text' | 'none'

const IMAGE = new Set([
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico', 'avif', 'apng',
])
const AUDIO = new Set(['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac', 'opus', 'weba'])
const VIDEO = new Set(['mp4', 'webm', 'ogv', 'mov', 'm4v', 'mkv', 'avi'])
const TEXT = new Set([
  'txt', 'log', 'md', 'markdown', 'json', 'yaml', 'yml', 'toml', 'ini', 'cfg',
  'conf', 'csv', 'tsv', 'xml', 'html', 'htm', 'css', 'scss', 'less', 'js', 'mjs',
  'cjs', 'ts', 'tsx', 'jsx', 'py', 'go', 'rs', 'java', 'kt', 'c', 'h', 'cc',
  'cpp', 'hpp', 'sh', 'bash', 'zsh', 'fish', 'sql', 'rb', 'php', 'swift', 'dart',
  'lua', 'r', 'pl', 'vue', 'svelte', 'dockerfile', 'makefile', 'env', 'gitignore',
])

export function extOf(name: string): string {
  const base = name.split('/').pop() ?? name
  const i = base.lastIndexOf('.')
  return i > 0 ? base.slice(i + 1).toLowerCase() : ''
}

/** Best-effort preview kind from the file name. */
export function previewKind(name: string): PreviewKind {
  const e = extOf(name)
  if (IMAGE.has(e)) return 'image'
  if (AUDIO.has(e)) return 'audio'
  if (VIDEO.has(e)) return 'video'
  if (TEXT.has(e)) return 'text'
  // Extensionless common text files.
  const base = (name.split('/').pop() ?? '').toLowerCase()
  if (base === 'dockerfile' || base === 'makefile' || base.startsWith('.') ) return 'text'
  return 'none'
}

/** MIME for a Blob URL so the browser renders media correctly. */
export function mimeFor(name: string): string {
  const e = extOf(name)
  const map: Record<string, string> = {
    png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif',
    webp: 'image/webp', bmp: 'image/bmp', svg: 'image/svg+xml', ico: 'image/x-icon',
    avif: 'image/avif', apng: 'image/apng',
    mp3: 'audio/mpeg', wav: 'audio/wav', ogg: 'audio/ogg', oga: 'audio/ogg',
    m4a: 'audio/mp4', aac: 'audio/aac', flac: 'audio/flac', opus: 'audio/opus',
    weba: 'audio/webm',
    mp4: 'video/mp4', webm: 'video/webm', ogv: 'video/ogg', mov: 'video/quicktime',
    m4v: 'video/x-m4v', mkv: 'video/x-matroska', avi: 'video/x-msvideo',
  }
  return map[e] ?? 'application/octet-stream'
}
