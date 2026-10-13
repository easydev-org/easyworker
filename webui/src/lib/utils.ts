import { type ClassValue, clsx } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// The AppTypography utilities (text-micro 10 / text-meta 12 / text-body 14,
// see app.css) are CUSTOM `@utility`s, so tailwind-merge's stock config does
// not know they are font-size classes. Left unrecognised, `cn('text-micro …',
// 'text-muted-foreground')` drops `text-micro` (it is mistaken for a
// conflicting text-* class) and the label silently renders at the inherited
// size. Register them in the font-size group explicitly.
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      'font-size': ['text-micro', 'text-meta', 'text-body'],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export type WithoutChild<T> = T extends { child?: unknown }
  ? Omit<T, 'child'>
  : T
export type WithoutChildren<T> = T extends { children?: unknown }
  ? Omit<T, 'children'>
  : T
export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>
export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & {
  ref?: U | null
}
