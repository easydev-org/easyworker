<script lang="ts">
  // ShellPage — a shell-style console. The worker is STATELESS (each Execute is
  // an independent interpreted job with no persistent cwd), so this keeps a
  // CLIENT-side virtual cwd and passes it as each command's workdir. Output
  // streams live via WatchJob; `Detach` disconnects the stream while the job
  // keeps running in the background (use the Jobs drawer to watch/kill it).
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import { session, client, resolveAbs, displayPath, stripAnsi } from '$lib/session.svelte'
  import { watchJob } from '$lib/worker'

  let termEl: HTMLDivElement | null = $state(null)
  let inputEl: HTMLInputElement | null = $state(null)
  let command = $state('')
  let running = $state(false)
  let watchAbort: AbortController | null = null
  const history: string[] = []
  let histIdx = -1

  interface Line {
    kind: 'cmd' | 'out' | 'err' | 'exit'
    text: string
  }
  let lines = $state<Line[]>([])

  function scrollBottom() {
    if (termEl) queueMicrotask(() => (termEl!.scrollTop = termEl!.scrollHeight))
  }
  function push(kind: Line['kind'], text: string) {
    lines = [...lines, { kind, text }]
    scrollBottom()
  }

  onMount(() => inputEl?.focus())

  async function run(cmd: string) {
    push('cmd', `${displayPath(session.cwd || session.workspace || '/')} $ ${cmd}`)
    if (cmd === 'clear') {
      lines = []
      return
    }
    // Lock the prompt for the whole command so nothing runs concurrently
    // (serialized shell). Detach releases it early for long jobs; the job keeps
    // running in the background.
    running = true
    const ac = new AbortController()
    watchAbort = ac
    try {
      const cd = cmd.match(/^cd(?:\s+(.+))?$/)
      if (cd) {
        await applyCd(cd[1])
        return
      }
      const r = await client().execute({ command: cmd, workdir: session.cwd || session.workspace })
      await watchJob(
        session.token,
        r.jobId,
        (ev) => {
          if (ev.output !== undefined) push('out', stripAnsi(ev.output))
          if (ev.done) push('exit', `[done exit=${ev.done.exitCode}]`)
        },
        ac.signal,
      )
    } catch (e) {
      if ((e as Error)?.name !== 'AbortError') push('err', `error: ${String((e as Error)?.message ?? e)}`)
    } finally {
      // Only the CURRENT run may clear the lock/controller (a detached run's
      // abort fires synchronously; guard against clobbering a newer run).
      if (watchAbort === ac) {
        watchAbort = null
        running = false
        queueMicrotask(() => inputEl?.focus())
      }
    }
  }

  async function applyCd(target: string | undefined) {
    const next = resolveAbs(session.cwd || session.workspace || '/', target)
    try {
      const r = await client().fileList({ path: next, depth: 1, limit: 1 })
      if (!r.isDir) {
        push('err', `cd: not a directory: ${target || '/'}`)
        return
      }
      session.cwd = next
    } catch (e) {
      push('err', `cd: ${String((e as Error)?.message ?? e)}`)
    }
  }

  function submit() {
    // One command at a time: while a job streams, the input is disabled, so a
    // second submit cannot race the first (which would overwrite watchAbort and
    // interleave two jobs' output in the transcript).
    if (running) return
    const cmd = command.trim()
    command = ''
    if (!cmd) return
    history.push(cmd)
    histIdx = history.length
    void run(cmd)
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (histIdx > 0) {
        histIdx--
        command = history[histIdx] ?? ''
      }
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (histIdx < history.length - 1) {
        histIdx++
        command = history[histIdx] ?? ''
      } else {
        histIdx = history.length
        command = ''
      }
    } else if (e.key === 'c' && e.ctrlKey) {
      detach()
    }
  }

  function detach() {
    // Stop streaming THIS command's output (history stays; no more lines are
    // appended). The job itself keeps running in the background — watch or kill
    // it from the Jobs drawer.
    if (watchAbort) {
      watchAbort.abort()
      push('exit', '[detached — output stopped; job continues in the background]')
    }
  }
</script>

<div class="flex min-h-0 min-w-0 flex-1 flex-col">
  <div bind:this={termEl} class="min-h-0 flex-1 overflow-auto px-3.5 py-3" aria-live="polite">
    {#each lines as ln, i (i)}
      <div
        class="term-line {ln.kind === 'cmd'
          ? 'text-primary'
          : ln.kind === 'err'
            ? 'text-destructive'
            : ln.kind === 'exit'
              ? 'text-muted-foreground'
              : ''}"
      >
        {ln.text}
      </div>
    {/each}
  </div>
  <form
    class="flex shrink-0 items-center gap-2 border-t border-border bg-card px-3 py-2"
    onsubmit={(e) => {
      e.preventDefault()
      submit()
    }}
  >
    <span class="shrink-0 truncate font-mono text-meta text-muted-foreground">{displayPath(session.cwd || session.workspace || '/')}</span>
    <span class="shrink-0 font-mono text-primary">$</span>
    <input
      bind:this={inputEl}
      bind:value={command}
      onkeydown={onKey}
      disabled={running}
      class="min-w-0 flex-1 border-0 bg-transparent p-0 font-mono text-meta outline-none placeholder:text-muted-foreground disabled:opacity-50"
      placeholder={running ? 'running…' : 'type a command, e.g. ls -la'}
      autocomplete="off"
      spellcheck="false"
    />
    <Button
      type="button"
      variant="outline"
      size="sm"
      class="shrink-0 gap-1.5"
      disabled={!running}
      title="Disconnect the output stream — the job keeps running in the background"
      onclick={detach}
    >
      <AppIcons.detach class="size-4" />Detach
    </Button>
  </form>
</div>
