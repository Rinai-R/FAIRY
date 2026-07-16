export function canStartPetWindowDrag({ button, desktopReady, petVisualOpen }) {
  return button === 0 && desktopReady === true && petVisualOpen === true;
}

export function startPetWindowDrag({
  event,
  desktopReady,
  petVisualOpen,
  startDragging,
  setDragging,
  onError,
  // Wails CSS drag arms on mousedown; preventDefault on pointerdown suppresses
  // that mouse event, so Wails must leave the pointer event unconsumed.
  consumePointerEvent = true,
}) {
  if (
    !canStartPetWindowDrag({
      button: event?.button,
      desktopReady,
      petVisualOpen,
    })
  ) {
    return false;
  }
  if (consumePointerEvent) {
    event.preventDefault?.();
    event.stopPropagation?.();
  }
  setDragging(true);
  startDragging().catch((error) => {
    setDragging(false);
    onError(error);
  });
  return true;
}
