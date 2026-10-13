// App-level reactive state: the bearer token, the connected worker's identity,
// the shell's virtual cwd, and the resolved theme. Kept in one place so the
// gate, shell, files and jobs surfaces share it.
import { setAnchors } from './paths'
import { createWorkerClient, type WorkerClient } from './worker'

const TOKEN_KEY = 'agent-worker.token'
const CWD_KEY = 'agent-worker.cwd'

export const session = $state({
  token: localStorage.getItem(TOKEN_KEY) ?? '',
  /** Absolute workspace root reported by Info, slash form ('' until known). */
  workspace: '',
  /** The worker user's home (Info.home); the OS-level `~`. */
  home: '',
  /** Absolute working directory of the shell (client-side only). */
  cwd: localStorage.getItem(CWD_KEY) ?? '',
  os: '',
  arch: '',
  bootId: '',
  /** What the sandbox IMAGE can do (Info.capabilities). */
  capabilities: null as null | {
    desktop?: boolean
    display?: string
    novnc?: boolean
    novncPort?: number
    xa11y?: boolean
    distro?: string
  },
  connected: false,
})

export function setToken(tok: string) {
  session.token = tok
  if (tok) localStorage.setItem(TOKEN_KEY, tok)
  else localStorage.removeItem(TOKEN_KEY)
}

export function setCwd(c: string) {
  session.cwd = c
  localStorage.setItem(CWD_KEY, c)
}

/** Apply the worker's identity and anchor the path helpers on its workspace. */
export function applyInfo(i: {
  workspace?: string
  home?: string
  os?: string
  arch?: string
  bootId?: string
  capabilities?: {
    desktop?: boolean
    display?: string
    novnc?: boolean
    novncPort?: number
    xa11y?: boolean
    distro?: string
  }
}) {
  session.workspace = i.workspace || '/'
  session.home = i.home || ''
  session.os = i.os || ''
  session.arch = i.arch || ''
  session.bootId = i.bootId || ''
  session.capabilities = i.capabilities ?? null
  setAnchors({ workspace: session.workspace })
}

export function client(): WorkerClient {
  return createWorkerClient(session.token)
}

export function fmtSize(n: number): string {
  n = Number(n) || 0
  if (n < 1024) return `${n} B`
  if (n < 1048576) return `${(n / 1024).toFixed(1)} KiB`
  return `${(n / 1048576).toFixed(1)} MiB`
}

export {
  absOf,
  basename,
  crumbsOf,
  displayPath,
  normAbs,
  resolveAbs,
  rootOf,
} from './paths'

/** Strip ANSI/VT escape sequences (CSI, OSC incl. hyperlinks, DCS, single). */
export function stripAnsi(s: string): string {
  return String(s)
    .replace(/\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)/g, '')
    .replace(/\u001bP[\s\S]*?\u001b\\/g, '')
    .replace(/\u001b[@-Z\\-_]/g, '')
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, '')
}
