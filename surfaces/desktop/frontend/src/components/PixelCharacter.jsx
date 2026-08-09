import "pixi.js/unsafe-eval";
import { Application, extend, useTick } from "@pixi/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { projectCharacterMotion, STATIC_CHARACTER_MOTION } from "../characterMotion.mjs";

extend({ Sprite });

function configureCharacterTextureLoader() {
  for (const parser of Assets.loader.parsers) {
    if (parser?.id === "texture" && parser.config) {
      parser.config.preferWorkers = false;
      parser.config.crossOrigin = "anonymous";
    }
  }
}

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
  motion,
  direction,
  displayScale,
  reducedMotion,
}) {
  const spriteRef = useRef(null);
  const elapsedRef = useRef(0);
  const projectionFailedRef = useRef(false);
  const renderScale = pixelTextureScale(visual, texture, displayScale);
  const coordinateScale = visual.scale * displayScale;
  const anchor = {
    x: visual.anchor.x / visual.frame.width,
    y: visual.anchor.y / visual.frame.height,
  };
  const originX = visual.anchor.x * coordinateScale;
  const originY = visual.anchor.y * coordinateScale;
  const directionScale = direction === "left" ? -1 : 1;

  const applyFrame = useCallback((frame) => {
    const sprite = spriteRef.current;
    if (sprite === null) return;
    sprite.x = originX + frame.offsetXRatio * visual.frame.width * coordinateScale;
    sprite.y = originY + frame.offsetYRatio * visual.frame.height * coordinateScale;
    sprite.scale.set(
      directionScale * renderScale.x * frame.scaleX,
      renderScale.y * frame.scaleY,
    );
  }, [coordinateScale, directionScale, originX, originY, renderScale.x, renderScale.y, visual.frame.height, visual.frame.width]);

  useTick(useCallback((ticker) => {
    if (projectionFailedRef.current) {
      applyFrame(STATIC_CHARACTER_MOTION);
      return;
    }
    elapsedRef.current += Math.max(0, ticker.deltaMS);
    try {
      applyFrame(projectCharacterMotion(motion, elapsedRef.current, reducedMotion));
    } catch (error) {
      projectionFailedRef.current = true;
      console.error("FAIRY_CHARACTER_MOTION_FAILURE", error);
      applyFrame(STATIC_CHARACTER_MOTION);
    }
  }, [applyFrame, motion, reducedMotion]));

  return (
    <pixiSprite
      ref={spriteRef}
      texture={texture}
      anchor={anchor}
      x={originX}
      y={originY}
      scale={{
        x: directionScale * renderScale.x,
        y: renderScale.y,
      }}
      eventMode="none"
    />
  );
}

function reducedMotionQuery() {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return null;
  }
  return window.matchMedia("(prefers-reduced-motion: reduce)");
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(() => reducedMotionQuery()?.matches === true);
  useEffect(() => {
    const query = reducedMotionQuery();
    if (!query) return undefined;
    const update = (event) => setReduced(event.matches);
    setReduced(query.matches);
    if (query.addEventListener) {
      query.addEventListener("change", update);
      return () => query.removeEventListener("change", update);
    }
    query.addListener?.(update);
    return () => query.removeListener?.(update);
  }, []);
  return reduced;
}

export function PixelCharacter({
  visual,
  visualState,
  direction = "right",
  displayScale = 1,
  onReady,
  onError,
}) {
  const [loaded, setLoaded] = useState(null);
  const loadedRef = useRef(null);
  const canvas = pixelCanvasSize(visual, displayScale);
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
  const reducedMotion = useReducedMotion();

  useEffect(() => {
    onReadyRef.current = onReady;
    onErrorRef.current = onError;
  }, [onError, onReady]);

  useEffect(() => {
    let disposed = false;
    let pendingRetained = true;
    configureCharacterTextureLoader();
    retainTexture(imageUrl);
    Assets.load(imageUrl)
      .then((loadedTexture) => {
        if (disposed) return;
        loadedTexture.source.scaleMode = "linear";
        const nextLoaded = Object.freeze({
          imageUrl,
          texture: loadedTexture,
          visualState: stateImage.id,
          motion: stateImage.motion || "still",
        });
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
  }, [imageUrl, stateImage.id, stateImage.motion]);

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
        key={`${visual.packId}:${renderable.visualState}:${renderable.imageUrl}`}
        visual={visual}
        texture={renderable.texture}
        motion={renderable.motion}
        direction={direction}
        displayScale={displayScale}
        reducedMotion={reducedMotion}
      />
    </Application>
  );
}
