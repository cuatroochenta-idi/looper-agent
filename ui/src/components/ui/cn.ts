/** Tiny class-name joiner. Falsy values are dropped. */
export function cn(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}
