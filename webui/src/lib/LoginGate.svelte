<script lang="ts">
  // LoginGate — the first thing shown. A bearer token is REQUIRED: a bad token
  // is rejected and the app is not entered. An unclaimed worker can instead be
  // claimed with its one-time code (WorkerEnroll), which returns a token.
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'
  import { session, setToken, client, applyInfo } from '$lib/session.svelte'
  import { createEnrollClient } from '$lib/worker'

  let { onEnter }: { onEnter: () => void } = $props()

  let token = $state('')
  let code = $state('')
  let needsCode = $state(false)
  let busy = $state(false)
  let err = $state('')

  async function verify(tok: string): Promise<boolean> {
    setToken(tok)
    try {
      const i = await client().info({})
      applyInfo(i)
      session.connected = true
      if (!session.cwd) session.cwd = session.workspace
      return true
    } catch (e) {
      setToken('')
      session.connected = false
      const m = String((e as { message?: string })?.message ?? e)
      err = /unauthenticated|permission/i.test(m)
        ? 'Invalid token — access denied.'
        : `Cannot reach the worker: ${m}`
      return false
    }
  }

  async function connect() {
    if (!token.trim() || busy) return
    busy = true
    err = ''
    const ok = await verify(token.trim())
    busy = false
    if (ok) onEnter()
  }

  async function claim() {
    if (!code.trim() || busy) return
    busy = true
    err = ''
    try {
      const r = await createEnrollClient().claim({ code: code.trim(), ownerId: 'webui' })
      token = r.token
      const ok = await verify(r.token)
      if (ok) onEnter()
    } catch (e) {
      err = `Claim failed: ${String((e as { message?: string })?.message ?? e)}`
    }
    busy = false
  }

  onMount(async () => {
    // If a token is already stored, verify it silently; a bad one drops back
    // to the gate (never enter with an invalid token).
    if (session.token) {
      const ok = await verify(session.token)
      if (ok) onEnter()
    }
    try {
      const s = await createEnrollClient().status({})
      needsCode = s.needsCode
    } catch {
      /* enroll optional */
    }
  })
</script>

<div class="flex h-dvh items-center justify-center p-5">
  <form
    class="flex w-full max-w-[420px] flex-col gap-4 rounded-xl border border-border bg-card p-6"
    onsubmit={(e) => {
      e.preventDefault()
      void connect()
    }}
  >
    <div class="flex items-center gap-3">
      <span
        class="flex size-10 items-center justify-center rounded-[10px] bg-primary font-mono text-[15px] font-bold text-primary-foreground"
        >AW</span
      >
      <div>
        <h1 class="text-lg font-semibold">Agent Worker</h1>
        <p class="text-meta text-muted-foreground">Enter your bearer token to continue.</p>
      </div>
    </div>

    <div class="flex flex-col gap-1.5">
      <Label for="gate-token">Bearer token</Label>
      <Input id="gate-token" type="password" bind:value={token} disabled={busy} placeholder="token" />
    </div>
    {#if err}<p class="text-meta text-destructive" role="alert">{err}</p>{/if}
    <Button type="submit" class="w-full" disabled={busy || !token.trim()}>
      {busy ? 'Connecting…' : 'Connect'}
    </Button>

    {#if needsCode}
      <div class="flex items-center gap-2 text-micro text-muted-foreground">
        <span class="h-px flex-1 bg-border"></span>or claim this worker<span class="h-px flex-1 bg-border"
        ></span>
      </div>
      <p class="text-meta text-muted-foreground">
        This worker is unclaimed. Paste the one-time code printed in its log to take exclusive
        ownership — you'll receive a token automatically.
      </p>
      <div class="flex flex-col gap-1.5">
        <Label for="gate-code">One-time code</Label>
        <Input id="gate-code" bind:value={code} disabled={busy} placeholder="code" />
      </div>
      <Button type="button" variant="outline" class="w-full" disabled={busy || !code.trim()} onclick={claim}>
        Claim
      </Button>
    {/if}
  </form>
</div>
