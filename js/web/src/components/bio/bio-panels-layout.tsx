import { getBioPanelsLayout } from "@streamplace/core";
import {
  cloneElement,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactElement,
} from "react";

export function BioPanelsLayout({
  children,
  dragging = false,
  keepColumnsWhileFocused = false,
  role,
}: {
  children: ReactElement<{ style?: CSSProperties; className?: string }>[];
  dragging?: boolean;
  keepColumnsWhileFocused?: boolean;
  role?: "list";
}) {
  const container = useRef<HTMLDivElement>(null);
  const order = children.map((child) => child.key).join();
  const [focused, setFocused] = useState(false);
  const [layout, setLayout] =
    useState<ReturnType<typeof getBioPanelsLayout>>(null);
  const previous = useRef<{
    width: number;
    heights: number[];
    order: string;
    keepColumns: boolean;
    layout: ReturnType<typeof getBioPanelsLayout>;
  } | null>(null);

  useLayoutEffect(() => {
    const element = container.current!;
    if (dragging) return;
    let active = true;
    const measure = () => {
      if (!active) return;
      const width = element.clientWidth;
      const cards = Array.from(element.children, (card) =>
        card.getBoundingClientRect(),
      );
      const heights = cards.map((card) => card.height);
      const last = previous.current;
      const sameCards = order === last?.order;
      const keepColumns =
        keepColumnsWhileFocused &&
        focused &&
        sameCards &&
        width === last?.width;
      if (
        last &&
        sameCards &&
        width === last.width &&
        keepColumns === last.keepColumns &&
        heights.every((height, i) => height === last.heights[i])
      )
        return;
      const next = getBioPanelsLayout(
        width,
        cards[0]?.width ?? width,
        Number.parseFloat(getComputedStyle(element).columnGap) || 0,
        heights,
        keepColumns ? last?.layout?.positions : undefined,
      );
      previous.current = { width, heights, order, keepColumns, layout: next };
      setLayout(next);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    for (const card of Array.from(element.children)) observer.observe(card);
    return () => {
      active = false;
      observer.disconnect();
    };
  }, [order, focused, keepColumnsWhileFocused, dragging]);

  return (
    <div
      ref={container}
      role={role}
      className="relative gap-4"
      style={{ height: layout?.containerHeight }}
      onFocusCapture={
        keepColumnsWhileFocused ? () => setFocused(true) : undefined
      }
      onBlurCapture={
        keepColumnsWhileFocused
          ? (event) => {
              if (!event.currentTarget.contains(event.relatedTarget))
                setFocused(false);
            }
          : undefined
      }
    >
      {children.map((child, index) => {
        const position = layout?.positions[index];
        return cloneElement(child, {
          className: `${child.props.className ?? ""} motion-safe:transition-[left,top] motion-safe:duration-200 motion-safe:ease-out motion-reduce:transition-none`,
          style: {
            ...child.props.style,
            ...(position
              ? { position: "absolute", left: position.x, top: position.y }
              : undefined),
          },
        });
      })}
    </div>
  );
}
