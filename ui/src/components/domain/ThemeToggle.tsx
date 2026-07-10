import { theme, toggleTheme } from "../../lib/state/theme";

export function ThemeToggle() {
  return (
    <button
      onClick={toggleTheme}
      title={`Switch to ${theme() === "dark" ? "light" : "dark"} theme`}
      aria-label="Toggle theme"
      class="flex h-8 w-8 items-center justify-center rounded-[8px] border border-line bg-bg-raised text-[15px] text-muted transition-colors hover:border-line-strong hover:text-text"
    >
      {theme() === "dark" ? "◐" : "◑"}
    </button>
  );
}
