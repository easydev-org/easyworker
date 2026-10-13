<script lang="ts">
  // FilesDrawer — a Finder/Explorer-style browser over the worker filesystem
  // (unconfined). The list shows the CURRENT directory's direct children; a
  // click on a directory ENTERS it (breadcrumb / ↑ go back). This is
  // deliberately decoupled from the shell's cwd: browsing here never moves the
  // shell. Drag-and-drop uploads files into the current directory.
  import { onDestroy, onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import { previewKind, mimeFor, type PreviewKind } from '$lib/file-types'
  import {
    client,
    session,
    normAbs,
    absOf,
    basename,
    displayPath,
    crumbsOf,
    fmtSize,
  } from '$lib/session.svelte'

  let { onClose }: { onClose: () => void } = $props()

  let filePicker: HTMLInputElement | null = $state(null)

  // Media previews load the whole file into one RPC/message; cap them so a
  // huge file cannot blow up memory/transport. Larger files offer Download only.
  const PREVIEW_MAX = 32 * 1024 * 1024

  interface Entry {
    path: string
    name: string
    isDir: boolean
    size: number
  }

  let dir = $state('')
  let entries = $state<Entry[]>([])
  let loading = $state(false)
  let err = $state('')
  let dragging = $state(false)
  let toast = $state('')

  // editor / viewer
  let editing = $state<string | null>(null)
  let editKind = $state<PreviewKind>('text')
  let editSize = $state(0)
  let mediaUrl = $state<string | null>(null)
  let mediaTooBig = $state(false)
  let content = $state('')
  let saving = $state(false)

  function say(msg: string) {
    toast = msg
    setTimeout(() => (toast = ''), 2500)
  }

  async function listDir(d: string): Promise<Entry[]> {
    const r = await client().fileList({ path: d, depth: 1, limit: 2000 })
    return (r.files ?? [])
      .map(f => {
        const abs = absOf(f.path)
        return { path: abs, name: basename(abs), isDir: f.isDir, size: Number(f.size) }
      })
      .sort((a, b) => (a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1))
  }

  async function load(d: string) {
    dir = normAbs(d)
    loading = true
    err = ''
    try {
      entries = await listDir(dir)
    } catch (e) {
      err = String((e as Error)?.message ?? e)
      entries = []
    }
    loading = false
  }

  /** Enter a directory (or open a file). */
  function activate(e: Entry) {
    if (e.isDir) void load(e.path)
    else void openFile(e.path, e.size)
  }

  /** Default location: the shell's cwd when it is inside the workspace, else
   *  the workspace root. (Read-only — does NOT change the shell.) */
  function defaultDir(): string {
    const ws = session.workspace || '/'
    const c = session.cwd
    if (c && (c === ws || c.startsWith(ws + '/'))) return c
    return ws
  }

  // Breadcrumb anchored on `~` (the workspace) when inside it, else the
  // filesystem root (`/`, or `C:` on Windows). Clicking a crumb navigates.
  const crumbs = $derived(crumbsOf(dir))

  function releaseMedia() {
    if (mediaUrl) {
      URL.revokeObjectURL(mediaUrl)
      mediaUrl = null
    }
  }

  async function openFile(path: string, size = 0) {
    releaseMedia()
    const kind = previewKind(path)
    editing = path
    editKind = kind === 'none' ? 'text' : kind
    editSize = size
    mediaTooBig = false
    content = ''
    try {
      // Media previews need the raw bytes (Blob URL); text is decoded.
      if (kind === 'image' || kind === 'audio' || kind === 'video') {
        if (size > PREVIEW_MAX) {
          mediaTooBig = true
          return
        }
        const r = await client().fileRead({ path })
        mediaUrl = URL.createObjectURL(new Blob([r.content as BlobPart], { type: mimeFor(path) }))
        return
      }
      const r = await client().fileRead({ path })
      content = new TextDecoder('utf-8', { fatal: false }).decode(r.content)
    } catch (e) {
      say(String((e as Error)?.message ?? e))
      editing = null
    }
  }

  async function save() {
    if (editing === null) return
    saving = true
    try {
      await client().fileWrite({ path: editing, content: new TextEncoder().encode(content) })
      say(`saved ${editing}`)
    } catch (e) {
      say(String((e as Error)?.message ?? e))
    }
    saving = false
  }

  function closeEditor() {
    releaseMedia()
    editing = null
  }

  async function del() {
    if (editing === null || !confirm(`Delete ${editing}?`)) return
    try {
      await client().fileDelete({ path: editing })
      closeEditor()
      await load(dir)
    } catch (e) {
      say(String((e as Error)?.message ?? e))
    }
  }

  async function download() {
    if (editing === null) return
    const a = document.createElement('a')
    a.href = mediaUrl ?? URL.createObjectURL(new Blob([new TextEncoder().encode(content)]))
    a.download = basename(editing)
    a.click()
    if (!mediaUrl) URL.revokeObjectURL(a.href)
  }

  /** "New file" opens the OS file picker and uploads the chosen file(s) into
   *  the current directory (same path as drag-drop), rather than asking for a
   *  name. */
  function newFile() {
    filePicker?.click()
  }

  function onPick(e: Event) {
    const input = e.currentTarget as HTMLInputElement
    const files = Array.from(input.files ?? [])
    input.value = ''
    void uploadFiles(files)
  }

  async function uploadFiles(files: File[]) {
    if (files.length === 0) return
    let ok = 0
    for (const f of files) {
      try {
        const buf = new Uint8Array(await f.arrayBuffer())
        await client().fileWrite({ path: normAbs(dir + '/' + f.name), content: buf })
        ok++
      } catch (e) {
        say(`upload ${f.name}: ${String((e as Error)?.message ?? e)}`)
      }
    }
    if (ok > 0) {
      say(`uploaded ${ok} file(s)`)
      await load(dir)
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault()
    dragging = false
    void uploadFiles(Array.from(e.dataTransfer?.files ?? []))
  }

  onMount(() => {
    void load(defaultDir())
  })

  onDestroy(releaseMedia)
</script>

<div
  class="relative flex min-h-0 min-w-0 flex-1 flex-col"
  role="presentation"
  ondragover={(e) => {
    e.preventDefault()
    dragging = true
  }}
  ondragleave={() => (dragging = false)}
  ondrop={onDrop}
>
  {#if editing === null}
    <div class="flex shrink-0 items-center gap-0.5 border-b border-border px-1.5 py-1">
      <Button variant="ghost" size="icon" title="Back (up one level)" aria-label="Back" onclick={() => load(normAbs(dir + '/..'))}>
        <AppIcons.back class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" title="Workspace (~)" aria-label="Workspace" onclick={() => load(session.workspace || '/')}>
        <AppIcons.home class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" title="Locate the shell's directory" aria-label="Locate" onclick={() => load(defaultDir())}>
        <AppIcons.locate class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" title="Upload file(s) here" aria-label="Upload" onclick={newFile}>
        <AppIcons.upload class="size-4" />
      </Button>
      <input bind:this={filePicker} type="file" multiple class="hidden" onchange={onPick} />
      <span class="ml-auto"></span>
      <Button variant="ghost" size="icon" title="Refresh" aria-label="Refresh" onclick={() => load(dir)}>
        <AppIcons.refresh class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" title="Close" aria-label="Close" onclick={onClose}>
        <AppIcons.close class="size-4" />
      </Button>
    </div>

    <div class="flex shrink-0 flex-wrap items-center gap-0.5 border-b border-border px-2 py-1 font-mono text-micro text-muted-foreground">
      {#each crumbs as c, i (c.path)}
        {#if i > 0}<AppIcons.chevronRight class="size-3 shrink-0 opacity-50" />{/if}
        <button type="button" class="rounded px-1 hover:bg-muted hover:text-foreground" onclick={() => load(c.path)}>
          {c.name}
        </button>
      {/each}
    </div>

    <div class="min-h-0 flex-1 overflow-auto p-1">
      {#if loading}
        <p class="p-2 text-meta text-muted-foreground">loading…</p>
      {:else if err}
        <p class="p-2 text-meta text-destructive">{err}</p>
      {:else if entries.length === 0}
        <p class="p-2 text-meta text-muted-foreground">empty — drop files here to upload</p>
      {:else}
        {#each entries as e (e.path)}
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-meta hover:bg-muted"
            onclick={() => activate(e)}
          >
            {#if e.isDir}
              <AppIcons.folder class="size-4 shrink-0 text-primary" />
            {:else}
              <AppIcons.file class="size-4 shrink-0 text-muted-foreground" />
            {/if}
            <span class="min-w-0 flex-1 truncate">{e.name}</span>
            <span class="shrink-0 text-micro text-muted-foreground">{e.isDir ? '' : fmtSize(e.size)}</span>
            {#if e.isDir}<AppIcons.chevronRight class="size-3.5 shrink-0 text-muted-foreground" />{/if}
          </button>
        {/each}
      {/if}
    </div>
  {:else}
    <div class="flex shrink-0 items-center gap-0.5 border-b border-border px-1.5 py-1">
      <Button variant="ghost" size="icon" title="Back to files" aria-label="Back" onclick={closeEditor}>
        <AppIcons.back class="size-4" />
      </Button>
      <span class="min-w-0 flex-1 truncate px-1 font-mono text-micro" title={editing}>{displayPath(editing)}</span>
      {#if editKind === 'text'}
        <Button variant="ghost" size="icon" title="Save" aria-label="Save" disabled={saving} onclick={save}>
          <AppIcons.save class="size-4" />
        </Button>
      {/if}
      <Button variant="ghost" size="icon" title="Download" aria-label="Download" onclick={download}>
        <AppIcons.download class="size-4" />
      </Button>
      <Button variant="ghost" size="icon" class="text-destructive" title="Delete" aria-label="Delete" onclick={del}>
        <AppIcons.delete class="size-4" />
      </Button>
    </div>
    {#if editKind === 'text'}
      <textarea
        bind:value={content}
        class="min-h-0 flex-1 resize-none rounded-none border-0 bg-transparent p-3 font-mono text-meta outline-none"
        spellcheck="false"
      ></textarea>
    {:else if mediaTooBig}
      <div class="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 p-4 text-center">
        <AppIcons.file class="size-8 text-muted-foreground" />
        <p class="text-meta text-muted-foreground">
          {fmtSize(editSize)} is too large to preview (limit {fmtSize(PREVIEW_MAX)}).
        </p>
        <Button variant="secondary" size="sm" onclick={download}>
          <AppIcons.download class="size-4" /> Download
        </Button>
      </div>
    {:else if mediaUrl && editKind === 'image'}
      <div class="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-muted/30 p-3">
        <img src={mediaUrl} alt={basename(editing)} class="max-h-full max-w-full object-contain" />
      </div>
    {:else if mediaUrl && editKind === 'video'}
      <div class="flex min-h-0 flex-1 items-center justify-center overflow-auto bg-black/90 p-2">
        <!-- svelte-ignore a11y_media_has_caption -->
        <video src={mediaUrl} controls class="max-h-full max-w-full"></video>
      </div>
    {:else if mediaUrl && editKind === 'audio'}
      <div class="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-4">
        <AppIcons.file class="size-8 text-muted-foreground" />
        <audio src={mediaUrl} controls class="w-full max-w-sm"></audio>
      </div>
    {/if}
  {/if}

  {#if dragging}
    <div class="pointer-events-none absolute inset-0 flex items-center justify-center bg-primary/10 text-primary">
      <span class="rounded-md border border-primary bg-background px-3 py-1.5 text-meta">Drop files to upload</span>
    </div>
  {/if}
  {#if toast}
    <div class="absolute inset-x-0 bottom-0 bg-popover px-3 py-1.5 text-micro text-foreground">{toast}</div>
  {/if}
</div>
