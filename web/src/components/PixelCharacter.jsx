import "pixi.js/unsafe-eval";
import { Application, extend } from "@pixi/react";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Assets,
  Sprite,
} from "pixi.js";

import {
  pixelCanvasSize,
  pixelTextureScale,
  resolveRenderablePixelTexture,
  resolveCharacterImageUrl,
  selectVisualStateImage,
} from "../pixelTexture.mjs";

extend({ Sprite });

const textureReferences = new Map();
const pendingTextureUnloads = new Map();

function retainTexture(imageUrl) {
  const pending = pendingTextureUnloads.get(imageUrl);
  if (pending !== undefined) {
    clearTimeout(pending);
    pendingTextureUnloads.delete(imageUrl);
  }
  textureReferences.set(imageUrl, (textureReferences.get(imageUrl) ?? 0) + 1);
}

function releaseTexture(imageUrl) {
  const nextReferences = (textureReferences.get(imageUrl) ?? 0) - 1;
  if (nextReferences > 0) {
    textureReferences.set(imageUrl, nextReferences);
    return;
  }
  textureReferences.delete(imageUrl);
  if (pendingTextureUnloads.has(imageUrl)) return;
  const timeout = setTimeout(() => {
    pendingTextureUnloads.delete(imageUrl);
    if (!textureReferences.has(imageUrl)) Assets.unload(imageUrl).catch(() => {});
  }, 250);
  pendingTextureUnloads.set(imageUrl, timeout);
}

function StaticStateImage({
  visual,
  texture,
  direction,
}) {
  const renderScale = pixelTextureScale(visual, texture);
  const anchor = {
    x: visual.anchor.x / visual.frame.width,
    y: visual.anchor.y / visual.frame.height,
  };

  return (
    <pixiSprite
      texture={texture}
      anchor={anchor}
      x={visual.anchor.x * visual.scale}
      y={visual.anchor.y * visual.scale}
      scale={{
        x: direction === "left" ? -renderScale.x : renderScale.x,
        y: renderScale.y,
      }}
      eventMode="none"
    />
  );
}

export function PixelCharacter({
  visual,
  visualState,
  direction = "right",
  onReady,
  onError,
}) {
  const [loaded, setLoaded] = useState(null);
  const loadedRef = useRef(null);
  const canvas = pixelCanvasSize(visual);
  const onReadyRef = useRef(onReady);
  const onErrorRef = useRef(onError);
  const stateImage = useMemo(
    () => selectVisualStateImage(visual, visualState),
    [visual, visualState],
  );
  const imageUrl = useMemo(
    () => resolveCharacterImageUrl(stateImage.imagePath, window.location.origin),
    [stateImage],
  );

  useEffect(() => {
    onReadyRef.current = onReady;
    onErrorRef.current = onError;
  }, [onError, onReady]);

  useEffect(() => {
    let disposed = false;
    let pendingRetained = true;
    retainTexture(imageUrl);
    Assets.load(imageUrl)
      .then((loadedTexture) => {
        if (disposed) return;
        loadedTexture.source.scaleMode = "linear";
        const nextLoaded = Object.freeze({ imageUrl, texture: loadedTexture });
        const previous = loadedRef.current;
        retainTexture(imageUrl);
        loadedRef.current = nextLoaded;
        setLoaded(nextLoaded);
        if (previous !== null) releaseTexture(previous.imageUrl);
        if (pendingRetained) {
          releaseTexture(imageUrl);
          pendingRetained = false;
        }
        onReadyRef.current();
      })
      .catch((error) => {
        console.error("FAIRY_CHARACTER_ASSET_FAILURE", error);
        if (pendingRetained) {
          releaseTexture(imageUrl);
          pendingRetained = false;
        }
        if (!disposed) onErrorRef.current(error);
      });
    return () => {
      disposed = true;
      if (pendingRetained) {
        releaseTexture(imageUrl);
        pendingRetained = false;
      }
    };
  }, [imageUrl]);

  useEffect(() => () => {
    const current = loadedRef.current;
    loadedRef.current = null;
    if (current !== null) releaseTexture(current.imageUrl);
  }, []);

  const renderable = resolveRenderablePixelTexture(loaded);
  if (renderable === null) return null;

  return (
    <Application
      width={canvas.width}
      height={canvas.height}
      backgroundAlpha={0}
      antialias
      autoDensity
      resolution={window.devicePixelRatio}
      preference="webgl"
    >
      <StaticStateImage
        key={`${visual.packId}:${visualState}`}
        visual={visual}
        texture={renderable.texture}
        direction={direction}
      />
    </Application>
  );
}
