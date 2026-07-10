import { cn } from "./cn";

export function Spinner(props: { class?: string; size?: number }) {
  const size = props.size ?? 14;
  return (
    <span
      class={cn("spin inline-block rounded-full border-2 border-current border-t-transparent", props.class)}
      style={{ width: `${size}px`, height: `${size}px` }}
      aria-label="loading"
    />
  );
}
