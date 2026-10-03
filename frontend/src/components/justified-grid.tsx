import {
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import justifiedLayout from "justified-layout";

// Aspect ratios outside this range are clamped (and cropped by the cell), so
// a panorama or a long screenshot can't take over a whole row.
const MIN_ASPECT = 0.5;
const MAX_ASPECT = 3;

/**
 * Lays items out in rows of equal height that fill the container width,
 * sized by each item's aspect ratio.
 */
export function JustifiedGrid<T>({
  items,
  getKey,
  getAspect,
  renderItem,
  targetRowHeight = 280,
  gap = 2,
  className,
}: {
  items: T[];
  getKey: (item: T) => React.Key;
  getAspect: (item: T) => number;
  renderItem: (item: T) => ReactNode;
  targetRowHeight?: number;
  gap?: number;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const observer = new ResizeObserver(([entry]) => {
      setWidth(entry.contentRect.width);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const layout = useMemo(
    () =>
      width > 0
        ? justifiedLayout(
            items.map((item) =>
              Math.min(MAX_ASPECT, Math.max(MIN_ASPECT, getAspect(item))),
            ),
            {
              containerWidth: width,
              containerPadding: 0,
              boxSpacing: gap,
              targetRowHeight,
            },
          )
        : undefined,
    [items, getAspect, width, targetRowHeight, gap],
  );

  return (
    <div className={className}>
      <div
        ref={ref}
        className="relative"
        style={{ height: layout?.containerHeight }}
      >
        {layout?.boxes.map((box, i) => (
          <div
            key={getKey(items[i])}
            className="absolute"
            style={{
              top: box.top,
              left: box.left,
              width: box.width,
              height: box.height,
            }}
          >
            {renderItem(items[i])}
          </div>
        ))}
      </div>
    </div>
  );
}
