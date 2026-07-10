import { splitProps, type JSX } from "solid-js";
import { cn } from "./cn";

export interface InputProps extends JSX.InputHTMLAttributes<HTMLInputElement> {
  mono?: boolean;
}

export function Input(props: InputProps) {
  const [local, rest] = splitProps(props, ["class", "mono"]);
  return (
    <input
      {...rest}
      class={cn(
        "h-9 w-full rounded-[8px] border border-line bg-input px-3 text-[13px] text-text " +
          "placeholder:text-faint transition-colors duration-150 " +
          "focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent/25",
        local.mono && "font-mono",
        local.class,
      )}
    />
  );
}
