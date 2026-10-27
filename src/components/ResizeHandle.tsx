import { useRef, type KeyboardEvent, type PointerEvent } from 'react';

interface ResizeHandleProps {
  /** id of the element whose height this handle controls */
  controls: string;
  /** accessible name, e.g. "Resize operation logs" */
  label: string;
  /** current height of the controlled element in pixels */
  height: number;
  minHeight: number;
  maxHeight: number;
  /** called while dragging or on keyboard resize */
  onResize: (height: number) => void;
  /** called once a drag or keyboard resize finishes */
  onResizeEnd?: (height: number) => void;
  /** called on double-click */
  onReset?: () => void;
}

const KEYBOARD_STEP = 40;

/**
 * A horizontal splitter below a panel, styled after PatternFly's resizable
 * drawer splitter: drag it (mouse, touch or pen) or use the arrow keys to
 * change the panel's height.
 */
export function ResizeHandle({
  controls,
  label,
  height,
  minHeight,
  maxHeight,
  onResize,
  onResizeEnd,
  onReset,
}: ResizeHandleProps) {
  const drag = useRef<{ startY: number; startHeight: number; last: number } | null>(null);
  const clamp = (value: number) => Math.round(Math.min(maxHeight, Math.max(minHeight, value)));

  const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const target = document.getElementById(controls);
    const startHeight = target ? target.getBoundingClientRect().height : height;
    drag.current = { startY: event.clientY, startHeight, last: startHeight };
    event.currentTarget.setPointerCapture(event.pointerId);
    event.currentTarget.focus();
  };

  const onPointerMove = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    const next = clamp(drag.current.startHeight + event.clientY - drag.current.startY);
    drag.current.last = next;
    onResize(next);
  };

  const onPointerUp = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    const { last } = drag.current;
    drag.current = null;
    event.currentTarget.releasePointerCapture(event.pointerId);
    onResizeEnd?.(last);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const next = {
      ArrowDown: height + KEYBOARD_STEP,
      ArrowUp: height - KEYBOARD_STEP,
      Home: minHeight,
      End: maxHeight,
    }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    const clamped = clamp(next);
    onResize(clamped);
    onResizeEnd?.(clamped);
  };

  return (
    <div
      className="mirror-gui-resize-handle"
      role="separator"
      aria-orientation="horizontal"
      aria-controls={controls}
      aria-label={label}
      aria-valuenow={Math.round(height)}
      aria-valuemin={minHeight}
      aria-valuemax={maxHeight}
      tabIndex={0}
      title="Drag to resize; double-click to reset"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onKeyDown={onKeyDown}
      onDoubleClick={onReset}
    >
      <span className="mirror-gui-resize-handle__grip" />
    </div>
  );
}
