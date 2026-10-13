<script lang="ts">
  // DesktopPage — the sandbox's live screen over WebCodecs + WebSocket.
  //
  // It IS the StreamPage: the "Desktop" tab shows the live screen (H.264 decoded
  // by the browser's VideoDecoder) with remote input. There is no noVNC / VNC
  // fallback any more — the stream is the only viewer.
  import StreamPage from '$lib/pages/StreamPage.svelte'

  let { onClose }: { onClose: () => void } = $props()
</script>

<StreamPage {onClose} />
