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
  event.preventDefault?.();
  event.stopPropagation?.();
  setDragging(true);
  startDragging().catch((error) => {
    setDragging(false);
    onError(error);
  });
  return true;
}
