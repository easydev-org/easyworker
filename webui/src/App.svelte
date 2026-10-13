<script lang="ts">
  // App — the root: a login gate, then the shell + mutually-exclusive
  // Files/Jobs drawers. On desktop the drawer is half the width; on phones it
  // is full-width so it is actually usable.
  import { onMount } from 'svelte'
  import { Button } from '$lib/components/ui/button'
  import { AppIcons } from '$lib/icons'
  import LoginGate from '$lib/LoginGate.svelte'
  import ShellPage from '$lib/pages/ShellPage.svelte'
  import FilesDrawer from '$lib/pages/FilesDrawer.svelte'
  import JobsDrawer from '$lib/pages/JobsDrawer.svelte'
  import DesktopPage from '$lib/pages/DesktopPage.svelte'
  import { session, setToken } from '$lib/session.svelte'

  let entered = $state(session.connected)
  let drawer = $state<'files' | 'jobs' | null>(null)
  // Main surface: the shell console or the graphical desktop (a live H.264
  // screen stream decoded by WebCodecs). Desktop appears only when the sandbox
  // has a screen (Info.capabilities.desktop).
  let surface = $state<'shell' | 'desktop'>('shell')
  const hasDesktop = $derived(!!session.capabilities?.desktop || !!session.capabilities?.novnc)

  onMount(() => {
    document.documentElement.dataset.theme = 'dark'
  })

  function signOut() {
    setToken('')
    session.connected = false
    session.workspace = ''
    entered = false
    drawer = null
  }

  function toggle(which: 'files' | 'jobs') {
    drawer = drawer === which ? null : which
  }

  function showDesktop() {
    drawer = null
    surface = surface === 'desktop' ? 'shell' : 'desktop'
  }
</script>

{#if !entered}
  <LoginGate onEnter={() => (entered = true)} />
{:else}
  <div class="flex h-dvh flex-col overflow-hidden bg-background">
    <header class="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-card px-3">
      <span class="flex items-center gap-2 font-semibold">
        <span class="flex size-5 items-center justify-center rounded-md bg-primary font-mono text-[10px] font-bold text-primary-foreground">AW</span>
        Agent Worker
      </span>
      <span class="hidden font-mono text-micro text-muted-foreground sm:inline">{session.os}/{session.arch}</span>
      <span class="ml-auto"></span>
      {#if hasDesktop}
        <Button
          variant="ghost"
          size="sm"
          class="gap-1.5 {surface === 'desktop' ? 'bg-muted text-foreground' : ''}"
          onclick={showDesktop}
        >
          <AppIcons.desktop class="size-4" />Desktop
        </Button>
      {/if}
      <Button
        variant="ghost"
        size="sm"
        class="gap-1.5 {drawer === 'files' ? 'bg-muted text-foreground' : ''}"
        onclick={() => toggle('files')}
      >
        <AppIcons.files class="size-4" />Files
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="gap-1.5 {drawer === 'jobs' ? 'bg-muted text-foreground' : ''}"
        onclick={() => toggle('jobs')}
      >
        <AppIcons.jobs class="size-4" />Jobs
      </Button>
      <Button variant="ghost" size="sm" class="gap-1.5" onclick={signOut}>
        <AppIcons.signOut class="size-4" /><span class="max-sm:hidden">Sign out</span>
      </Button>
    </header>

    <div class="relative flex min-h-0 flex-1">
      {#if surface === 'desktop'}
        <DesktopPage onClose={() => (surface = 'shell')} />
      {:else}
        <ShellPage />
      {/if}
      {#if surface === 'shell' && drawer === 'files'}
        <aside class="flex min-h-0 w-full flex-col border-border bg-card max-sm:absolute max-sm:inset-0 max-sm:z-10 max-sm:border-l-0 sm:w-1/2 sm:border-l">
          <FilesDrawer onClose={() => (drawer = null)} />
        </aside>
      {:else if surface === 'shell' && drawer === 'jobs'}
        <aside class="flex min-h-0 w-full flex-col border-border bg-card max-sm:absolute max-sm:inset-0 max-sm:z-10 max-sm:border-l-0 sm:w-1/2 sm:border-l">
          <JobsDrawer onClose={() => (drawer = null)} />
        </aside>
      {/if}
    </div>
  </div>
{/if}
