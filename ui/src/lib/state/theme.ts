import { createSignal } from "solid-js";

export type Theme = "dark" | "light";
const KEY = "looper-theme";

function initial(): Theme {
  const attr = document.documentElement.getAttribute("data-theme");
  if (attr === "light" || attr === "dark") return attr;
  return "dark";
}

const [theme, setThemeSignal] = createSignal<Theme>(initial());

export { theme };

export function setTheme(next: Theme) {
  setThemeSignal(next);
  document.documentElement.setAttribute("data-theme", next);
  document.documentElement.style.background = next === "light" ? "#f6f7f9" : "#0a0d12";
  try {
    localStorage.setItem(KEY, next);
  } catch {
    /* private mode — ignore */
  }
}

export function toggleTheme() {
  setTheme(theme() === "dark" ? "light" : "dark");
}
