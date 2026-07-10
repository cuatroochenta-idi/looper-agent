import { splitProps, type JSX } from "solid-js";
import { cn } from "./cn";

export interface CardProps extends JSX.HTMLAttributes<HTMLDivElement> {
  interactive?: boolean;
  inset?: boolean;
}

export function Card(props: CardProps) {
  const [local, rest] = splitProps(props, ["interactive", "inset", "class", "children"]);
  return (
    <div
      {...rest}
      class={cn(
        "rounded-[12px] border border-line bg-card shadow-[var(--shadow-card)]",
        local.inset && "bg-sunken",
        local.interactive &&
          "cursor-pointer transition-colors duration-150 hover:bg-card-hover hover:border-line-strong",
        local.class,
      )}
    >
      {local.children}
    </div>
  );
}
