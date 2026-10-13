// worker.v1 Connect client for the control panel. The panel is SAME-ORIGIN
// with the worker (served from its own root), so the base URL is the page's
// origin and the browser needs only a bearer token — no CORS.
//
// The worker serves plain Connect over h1 + h2c. connect-web's default
// transport speaks the Connect JSON protocol over h1, which the worker
// accepts; server-streaming (WatchJob) is handled by a small manual frame
// reader (see watchJob).

import {
  type Client,
  createClient,
  type Interceptor,
} from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { WorkerEnroll, WorkerService } from './gen/worker/v1/worker_pb.js'

export type WorkerClient = Client<typeof WorkerService>
export type EnrollClient = Client<typeof WorkerEnroll>

function bearer(token: string): Interceptor {
  return next => async req => {
    if (token) req.header.set('Authorization', `Bearer ${token}`)
    return await next(req)
  }
}

export function createWorkerClient(token: string): WorkerClient {
  const transport = createConnectTransport({
    baseUrl: typeof location !== 'undefined' ? location.origin : '',
    interceptors: [bearer(token)],
  })
  return createClient(WorkerService, transport)
}

export function createEnrollClient(): EnrollClient {
  const transport = createConnectTransport({
    baseUrl: typeof location !== 'undefined' ? location.origin : '',
  })
  return createClient(WorkerEnroll, transport)
}

// ---- server-streaming WatchJob over the Connect streaming protocol ----
// Each frame is a 5-byte envelope: 1 flag byte + 4-byte big-endian length,
// followed by the payload. flag&0x02 = end-of-stream, flag&0x01 = error.

export interface WatchEvent {
  output?: string
  done?: { exitCode: number }
}

export async function watchJob(
  token: string,
  jobId: string,
  onEvent: (ev: WatchEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/worker.v1.WorkerService/WatchJob', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/connect+json',
      'Connect-Protocol-Version': '1',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: frame(JSON.stringify({ jobId })) as unknown as BodyInit,
    signal,
  })
  if (!res.ok || !res.body) {
    throw new Error(
      `watch failed: HTTP ${res.status} ${(await res.text()).slice(0, 200)}`,
    )
  }
  const reader = res.body.getReader()
  let buf: Uint8Array<ArrayBufferLike> = new Uint8Array(0)
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buf = concat(buf, value)
    while (buf.length >= 5) {
      const len = (buf[1]! << 24) | (buf[2]! << 16) | (buf[3]! << 8) | buf[4]!
      if (buf.length < 5 + len) break
      const flag = buf[0]!
      const payload = new TextDecoder().decode(buf.subarray(5, 5 + len))
      buf = buf.subarray(5 + len)
      if (flag & 0x02) continue
      if (flag & 0x01) {
        let m = payload
        try {
          m = JSON.parse(payload).message || payload
        } catch {
          /* raw */
        }
        throw new Error(m)
      }
      let msg: { output?: string; done?: { exitCode?: number } }
      try {
        msg = JSON.parse(payload)
      } catch {
        continue
      }
      if (msg.output !== undefined) onEvent({ output: msg.output })
      if (msg.done) {
        onEvent({ done: { exitCode: msg.done.exitCode ?? 0 } })
        return
      }
    }
  }
}

function frame(str: string): Uint8Array {
  const bytes = new TextEncoder().encode(str)
  const out = new Uint8Array(5 + bytes.length)
  out[1] = (bytes.length >>> 24) & 255
  out[2] = (bytes.length >>> 16) & 255
  out[3] = (bytes.length >>> 8) & 255
  out[4] = bytes.length & 255
  out.set(bytes, 5)
  return out
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const o = new Uint8Array(a.length + b.length)
  o.set(a)
  o.set(b, a.length)
  return o
}
