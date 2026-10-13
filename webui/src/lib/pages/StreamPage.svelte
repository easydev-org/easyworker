<script lang="ts">
  // StreamPage — the sandbox's live screen over WebCodecs + WebSocket.
  //
  // The worker streams H.264 access units (Annex-B) over a WebSocket — the SAME
  // 443/TCP the page already uses — and this page decodes each one with the
  // WebCodecs `VideoDecoder`, painting a canvas. NO WebRTC / UDP / STUN / TURN /
  // ICE. Remote input + clipboard ride the same socket (the control protocol is
  // unchanged; only the transport differs from the old DataChannel).
  import { AppIcons } from '$lib/icons'

  let { onClose }: { onClose: () => void } = $props()

  let canvas = $state<HTMLCanvasElement | null>(null)
  let state = $state<'connecting' | 'live' | 'error'>('connecting')
  let error = $state('')
  let clipboard = $state('')
  let showSysKeys = $state(false)
  let ws: WebSocket | null = null
  let decoder: VideoDecoder | null = null
  let ctx2d: CanvasRenderingContext2D | null = null
  let codec = 'avc1.42E01F'
  // Source (captured) pixel size. $state so `screenRect` recomputes once the
  // stream reports its size (a plain var left the canvas stretched).
  let srcW = $state(0)
  let srcH = $state(0)
  let cursorX = $state(0)
  let cursorY = $state(0)
  let cursorOn = $state(false)
  let boxW = $state(0)
  let boxH = $state(0)
  // FIT WIDTH: the screen spans the full container width, height = width /
  // aspect. Keeps the layout stable when the mobile keyboard changes the
  // viewport HEIGHT (a contain-fit would rescale and shift the screen).
  const screenRect = $derived.by(() => {
    if (!boxW || !srcW || !srcH) return { left: 0, top: 0, width: boxW, height: boxH }
    const dh = Math.round((srcH / srcW) * boxW)
    return { left: 0, top: 0, width: boxW, height: dh }
  })

  // ---- control protocol (must match internal/a11y/control.go) ----
  const T = {
    move: 0x01, down: 0x02, up: 0x03, wheel: 0x04,
    keyDown: 0x05, keyUp: 0x06, text: 0x07, hello: 0x08,
    clipSet: 0x10, clipGet: 0x11, clipData: 0x12,
    sysKey: 0x20, gesture: 0x21,
  }
  function send(bytes: Uint8Array) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(bytes)
  }
  function i32(v: number): number[] {
    const b = new DataView(new ArrayBuffer(4))
    b.setInt32(0, v, true)
    return [b.getUint8(0), b.getUint8(1), b.getUint8(2), b.getUint8(3)]
  }
  // Map a DOM event position inside the canvas to source pixels.
  function toSource(e: MouseEvent | Touch): [number, number] {
    const el = canvas
    if (!el) return [0, 0]
    const r = el.getBoundingClientRect()
    const vw = srcW || el.width || 1
    const vh = srcH || el.height || 1
    const x = Math.round(((e.clientX - r.left) / r.width) * vw)
    const y = Math.round(((e.clientY - r.top) / r.height) * vh)
    return [Math.max(0, Math.min(vw, x)), Math.max(0, Math.min(vh, y))]
  }
  function setCursorFrom(clientX: number, clientY: number) {
    const el = canvas
    if (!el) return
    const r = el.getBoundingClientRect()
    cursorX = Math.max(0, Math.min(screenRect.width, clientX - r.left))
    cursorY = Math.max(0, Math.min(screenRect.height, clientY - r.top))
    cursorOn = true
  }

  function onMouseMove(e: MouseEvent) {
    setCursorFrom(e.clientX, e.clientY)
    const [x, y] = toSource(e)
    send(new Uint8Array([T.move, ...i32(x), ...i32(y)]))
  }
  function onMouseDown(e: MouseEvent) {
    e.preventDefault()
    const [x, y] = toSource(e)
    send(new Uint8Array([T.down, ...i32(x), ...i32(y), e.button & 0xff]))
    ;(e.currentTarget as HTMLElement).focus({ preventScroll: true })
  }
  function onMouseUp(e: MouseEvent) { send(new Uint8Array([T.up, e.button & 0xff])) }
  function onMouseLeave() { cursorOn = false }
  function onWheel(e: WheelEvent) {
    e.preventDefault()
    const [x, y] = toSource(e)
    send(new Uint8Array([T.wheel, ...i32(x), ...i32(y), ...i32(e.deltaX), ...i32(e.deltaY)]))
  }
  // The right button is already delivered by onMouseDown/onMouseUp; here we
  // ONLY suppress the browser menu.
  function onContextMenu(e: MouseEvent) { e.preventDefault() }

  // ---- touch: tap = left click, long-press = right click, move = drag ----
  let touchActive = false
  let touchMoved = false
  let lastTouchX = 0
  let lastTouchY = 0
  let startTouchX = 0
  let startTouchY = 0
  let touchStartAt = 0

  function onTouchStart(e: TouchEvent) {
    e.preventDefault()
    if (e.touches.length !== 1) return
    const [x, y] = toSource(e.touches[0])
    setCursorFrom(e.touches[0].clientX, e.touches[0].clientY)
    startTouchX = x; startTouchY = y
    lastTouchX = x; lastTouchY = y
    touchActive = true; touchMoved = false; touchStartAt = Date.now()
    send(new Uint8Array([T.move, ...i32(x), ...i32(y)]))
  }
  function onTouchMove(e: TouchEvent) {
    e.preventDefault()
    if (!touchActive || e.touches.length !== 1) return
    const [x, y] = toSource(e.touches[0])
    // Follow the cursor during a drag (it only moved on touchstart before, so
    // it sat frozen at the origin while the window moved).
    setCursorFrom(e.touches[0].clientX, e.touches[0].clientY)
    if (!touchMoved && (Math.abs(x - lastTouchX) > 2 || Math.abs(y - lastTouchY) > 2)) {
      touchMoved = true
      // Press at the TOUCH START point: pressing at the already-moved point
      // missed the titlebar, so a titlebar drag pressed the client instead.
      send(new Uint8Array([T.down, ...i32(startTouchX), ...i32(startTouchY), 0]))
    }
    lastTouchX = x; lastTouchY = y
    send(new Uint8Array([T.move, ...i32(x), ...i32(y)]))
  }
  function onTouchEnd(e: TouchEvent) {
    e.preventDefault()
    if (!touchActive) return
    touchActive = false
    const held = Date.now() - touchStartAt
    if (touchMoved) {
      send(new Uint8Array([T.up, 0]))
    } else if (held >= 600) {
      // Long-press = RIGHT click (context menu).
      send(new Uint8Array([T.down, ...i32(lastTouchX), ...i32(lastTouchY), 2]))
      send(new Uint8Array([T.up, 2]))
    } else {
      // Tap = LEFT click.
      send(new Uint8Array([T.down, ...i32(lastTouchX), ...i32(lastTouchY), 0]))
      send(new Uint8Array([T.up, 0]))
    }
  }

  // ---- virtual keyboard ----
  let kbd = $state<HTMLTextAreaElement | null>(null)
  function showKeyboard() { kbd?.focus({ preventScroll: true }) }
  let sticky = $state(0)
  function toggleSticky(bit: number) { sticky ^= bit }
  function sendText(text: string) {
    if (text) send(new Uint8Array([T.text, ...new TextEncoder().encode(text)]))
  }
  function sendKey(name: string) {
    const nb = new TextEncoder().encode(name)
    send(new Uint8Array([T.keyDown, nb.length, ...nb, ...i32(0), 0]))
    send(new Uint8Array([T.keyUp, nb.length, ...nb, ...i32(0), 0]))
  }
  // Soft keyboard: like noVNC, keys come from keydown/keyup (whose `code`/`key`
  // name the PHYSICAL key), so Backspace and Delete are distinguishable.
  // preventDefault() stops the hidden textarea accumulating characters, so
  // `input` only fires for IME/CJK commits, sent as TEXT.
  function onSoftKeyDown(e: KeyboardEvent) {
    if (e.isComposing || e.keyCode === 229) return
    const { name, char } = keyFor(e)
    if (!name && !char) return
    e.preventDefault()
    const nb = new TextEncoder().encode(name)
    send(new Uint8Array([T.keyDown, nb.length, ...nb, ...i32(char), mods(e) | sticky]))
    sticky = 0
  }
  function onSoftKeyUp(e: KeyboardEvent) {
    if (e.isComposing || e.keyCode === 229) return
    const { name, char } = keyFor(e)
    if (!name && !char) return
    e.preventDefault()
    const nb = new TextEncoder().encode(name)
    send(new Uint8Array([T.keyUp, nb.length, ...nb, ...i32(char), mods(e)]))
  }
  function onSoftInput(e: Event) {
    const el = e.target as HTMLTextAreaElement
    const val = el.value
    if (val) { sendText(val); el.value = '' }
  }
  function mods(e: KeyboardEvent): number {
    return (e.shiftKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.altKey ? 4 : 0) | (e.metaKey ? 8 : 0)
  }
  const NAMED: Record<string, string> = {
    Enter: 'Return', NumpadEnter: 'Return', Escape: 'Escape', Tab: 'Tab',
    Backspace: 'Backspace', Delete: 'Delete', Space: 'Space',
    ArrowUp: 'Up', ArrowDown: 'Down', ArrowLeft: 'Left', ArrowRight: 'Right',
    Home: 'Home', End: 'End', PageUp: 'PageUp', PageDown: 'PageDown',
    ShiftLeft: 'Shift', ShiftRight: 'Shift', ControlLeft: 'Ctrl', ControlRight: 'Ctrl',
    AltLeft: 'Alt', AltRight: 'Alt', MetaLeft: 'Meta', MetaRight: 'Meta',
  }
  function keyFor(e: KeyboardEvent): { name: string; char: number } {
    const named = NAMED[e.code]
    if (named) return { name: named, char: 0 }
    if (e.key.length === 1) return { name: '', char: e.key.codePointAt(0) ?? 0 }
    return { name: '', char: 0 }
  }
  function onKeyDown(e: KeyboardEvent) {
    const { name, char } = keyFor(e)
    if (!name && !char) return
    e.preventDefault()
    const nb = new TextEncoder().encode(name)
    send(new Uint8Array([T.keyDown, nb.length, ...nb, ...i32(char), mods(e) | sticky]))
    sticky = 0
  }
  function onKeyUp(e: KeyboardEvent) {
    const { name, char } = keyFor(e)
    if (!name && !char) return
    e.preventDefault()
    const nb = new TextEncoder().encode(name)
    send(new Uint8Array([T.keyUp, nb.length, ...nb, ...i32(char), mods(e)]))
  }
  function sendSystemKey(name: string) { const nb = new TextEncoder().encode(name); send(new Uint8Array([T.sysKey, nb.length, ...nb])) }
  function sendClipboard() { send(new Uint8Array([T.clipSet, ...new TextEncoder().encode(clipboard)])) }
  function requestClipboard() { send(new Uint8Array([T.clipGet])) }

  function paint(frame: VideoFrame) {
    const el = canvas
    if (!el) { frame.close(); return }
    if (el.width !== frame.displayWidth || el.height !== frame.displayHeight) {
      el.width = frame.displayWidth
      el.height = frame.displayHeight
    }
    srcW = frame.displayWidth
    srcH = frame.displayHeight
    if (!ctx2d) ctx2d = el.getContext('2d')
    ctx2d?.drawImage(frame, 0, 0)
    frame.close()
  }
  function hasVCLIDR(b: Uint8Array): boolean {
    let i = 0
    while (i + 3 < b.length) {
      if (b[i] === 0 && b[i + 1] === 0 && (b[i + 2] === 1 || (b[i + 2] === 0 && b[i + 3] === 1))) {
        const off = b[i + 2] === 1 ? i + 3 : i + 4
        if (off < b.length && (b[off] & 0x1f) === 5) return true
        i = off
        continue
      }
      i++
    }
    return false
  }

  async function start() {
    stop()
    state = 'connecting'
    error = ''
    if (typeof VideoDecoder === 'undefined') {
      state = 'error'
      error = 'WebCodecs (VideoDecoder) is not supported — needs a secure (HTTPS) context and a modern browser'
      return
    }
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const sock = new WebSocket(`${proto}//${location.host}/stream/ws`)
    sock.binaryType = 'arraybuffer'
    ws = sock
    let gotKey = false
    sock.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        try { const h = JSON.parse(ev.data); if (h.codec) codec = h.codec; if (h.w) srcW = h.w; if (h.h) srcH = h.h } catch { /* ignore */ }
        decoder = new VideoDecoder({ output: paint, error: (e) => { state = 'error'; error = String(e) } })
        decoder.configure({ codec, optimizeForLatency: true })
        state = 'live'
        return
      }
      const buf = new Uint8Array(ev.data as ArrayBuffer)
      if (!decoder || decoder.state !== 'configured') return
      const isKey = hasVCLIDR(buf)
      if (!isKey && !gotKey) return // wait for the first key frame
      if (isKey) gotKey = true
      try {
        decoder.decode(new EncodedVideoChunk({ type: isKey ? 'key' : 'delta', timestamp: performance.now() * 1000, data: buf }))
      } catch { /* drop */ }
    }
    sock.onerror = () => { state = 'error'; error = 'websocket error' }
    sock.onclose = () => { if (state !== 'error') state = 'error' }
  }
  function stop() {
    if (decoder) { try { decoder.close() } catch { /* noop */ } decoder = null }
    if (ws) { try { ws.close() } catch { /* noop */ } ws = null }
  }

  $effect(() => {
    start()
    return () => stop()
  })
</script>

<div class="flex min-h-0 min-w-0 flex-1 flex-col bg-background">
  <header class="flex h-10 min-w-0 shrink-0 items-center gap-2 overflow-x-auto border-b border-border bg-card px-3">
    <AppIcons.stream class="size-4" />
    <span class="font-medium">Stream</span>
    <span class="font-mono text-micro text-muted-foreground">/stream/ws</span>
    <span class="rounded px-1.5 py-0.5 text-micro {state === 'live' ? 'bg-green-500/15 text-green-600' : state === 'error' ? 'bg-destructive/15 text-destructive' : 'bg-muted text-muted-foreground'}">
      {state === 'live' ? 'live' : state === 'error' ? 'error' : 'connecting'}
    </span>
    <span class="ml-auto"></span>
    <button class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-sm hover:bg-muted" onclick={showKeyboard}>
      <AppIcons.jobs class="size-4" />Keyboard
    </button>
    <button class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-sm hover:bg-muted" onclick={() => (showSysKeys = !showSysKeys)}>
      <AppIcons.jobs class="size-4" />Keys
    </button>
    <button class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-sm hover:bg-muted" onclick={start}>
      <AppIcons.refresh class="size-4" />Retry
    </button>
    <button class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-sm hover:bg-muted" onclick={onClose}>
      <AppIcons.close class="size-4" />Close
    </button>
  </header>

  {#if state === 'error' && error}
    <div class="shrink-0 border-b border-destructive/40 bg-destructive/10 px-3 py-1.5 text-sm text-destructive">{error}</div>
  {/if}

  {#if showSysKeys}
    <div class="flex shrink-0 flex-wrap items-center gap-1 border-b border-border bg-card px-3 py-1.5">
      {#each ['home', 'back', 'recents', 'menu', 'notifications', 'volume_up', 'volume_down', 'volume_mute', 'power'] as k (k)}
        <button class="rounded border border-border px-2 py-0.5 text-micro hover:bg-muted" onclick={() => sendSystemKey(k)}>{k}</button>
      {/each}
    </div>
  {/if}

  <textarea bind:this={kbd} oninput={onSoftInput} onkeydown={onSoftKeyDown} onkeyup={onSoftKeyUp} class="pointer-events-none absolute h-px w-px opacity-0" aria-hidden="true"></textarea>

  <div
    class="relative min-h-0 w-full flex-1 overflow-y-auto overflow-x-hidden bg-neutral-800"
    bind:clientWidth={boxW}
    bind:clientHeight={boxH}
  >
    <!-- The canvas IS the screen rect (aspect-preserving, centered on width):
         a <canvas> is not a replaced element, so `object-contain` would NOT
         letterbox it — sizing the element to the fitted rect keeps the aspect
         correct on any viewport. -->
    <canvas
      bind:this={canvas}
      tabindex="0"
      class="absolute cursor-none touch-none outline-none"
      style="left:{screenRect.left}px; top:{screenRect.top}px; width:{screenRect.width}px; height:{screenRect.height}px"
      onmousemove={onMouseMove}
      onmousedown={onMouseDown}
      onmouseup={onMouseUp}
      onmouseleave={onMouseLeave}
      onwheel={onWheel}
      oncontextmenu={onContextMenu}
      ontouchstart={onTouchStart}
      ontouchmove={onTouchMove}
      ontouchend={onTouchEnd}
      onkeydown={onKeyDown}
      onkeyup={onKeyUp}
    ></canvas>
    {#if cursorOn}
      <div class="pointer-events-none absolute z-10" style="left:{screenRect.left + cursorX}px; top:{screenRect.top + cursorY}px">
        <svg width="16" height="16" viewBox="0 0 16 16" class="text-white drop-shadow"><path fill="currentColor" stroke="black" stroke-width="1" d="M1 1l6 14 2-6 6-2z"/></svg>
      </div>
    {/if}
  </div>

  <footer class="flex min-w-0 shrink-0 items-center gap-2 overflow-x-auto border-t border-border bg-card px-3 py-1.5">
    <input class="w-44 rounded border border-border bg-background px-1.5 py-0.5 text-micro" bind:value={clipboard} placeholder="clipboard" />
    <button class="rounded border border-border px-2 py-0.5 text-micro hover:bg-muted" onclick={sendClipboard}>Set</button>
    <button class="rounded border border-border px-2 py-0.5 text-micro hover:bg-muted" onclick={requestClipboard}>Get</button>
  </footer>
</div>
