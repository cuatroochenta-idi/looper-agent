import { splitProps, type JSX } from "solid-js";
import { cn } from "./cn";

type Variant = "primary" | "ghost" | "outline" | "subtle" | "danger";
type Size = "sm" | "md" | "icon";

export interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
}

const base =
  "inline-flex items-center justify-center gap-1.5 rounded-[8px] font-medium " +
  "transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 " +
  "focus-visible:ring-accent/60 disabled:opacity-45 disabled:pointer-events-none select-none";

const variants: Record<Variant, string> = {
  primary: "bg-accent text-[#0a0d12] hover:brightness-110 shadow-[0_1px_0_rgba(255,255,255,0.15)_inset]",
  ghost: "text-muted hover:text-text hover:bg-input/70",
  outline: "border border-line-strong text-text hover:bg-input/60 hover:border-accent-line",
  subtle: "bg-input text-text hover:bg-card-hover border border-line",
  danger: "bg-danger-soft text-danger hover:bg-danger/20 border border-danger/30",
};

const sizes: Record<Size, string> = {
  sm: "h-7 px-2.5 text-[12px]",
  md: "h-9 px-3.5 text-[13px]",
  icon: "h-8 w-8 text-[14px]",
};

export function Button(props: ButtonProps) {
  const [local, rest] = splitProps(props, ["variant", "size", "class", "children"]);
  return (
    <button
      {...rest}
      class={cn(base, variants[local.variant ?? "subtle"], sizes[local.size ?? "md"], local.class)}
    >
      {local.children}
    </button>
  );
}
