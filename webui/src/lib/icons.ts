// Semantic icon slots for the worker control panel — one name per meaning, so
// call sites read `AppIcons.folder` rather than a lucide id. Mirrors the
// convention in abcp-sdk/webui/src/lib/icons.ts (a curated subset here).
import {
  ArrowLeft,
  Ban,
  ChevronDown,
  ChevronRight,
  Download,
  Eye,
  FileText,
  Folder,
  FolderOpen,
  FolderTree,
  House,
  ListChecks,
  LocateFixed,
  LogOut,
  Monitor,
  Play,
  RefreshCw,
  Radio,
  Save,
  Trash,
  Unplug,
  Upload,
  X,
} from '@lucide/svelte'

export const AppIcons = {
  // top bar / drawers
  files: FolderTree,
  jobs: ListChecks,
  signOut: LogOut,
  close: X,
  // files
  home: House,
  upload: Upload,
  locate: LocateFixed,
  folder: Folder,
  folderOpen: FolderOpen,
  file: FileText,
  chevronRight: ChevronRight,
  chevronDown: ChevronDown,
  save: Save,
  download: Download,
  delete: Trash,
  back: ArrowLeft,
  refresh: RefreshCw,
  // jobs
  view: Eye,
  watch: Play,
  kill: Ban,
  // shell
  detach: Unplug,
  // desktop (noVNC)
  desktop: Monitor,
  // desktop (WebRTC live stream)
  stream: Radio,
} as const

export type AppIcon = (typeof AppIcons)[keyof typeof AppIcons]
