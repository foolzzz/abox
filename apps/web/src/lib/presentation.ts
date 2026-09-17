import type { PresentationContext } from "../api/types";

export function currentPresentationContext(surface = "conversation"): PresentationContext {
  const width = Math.max(0, Math.round(window.innerWidth));
  const height = Math.max(0, Math.round(window.innerHeight));
  const deviceClass = width < 640 ? "mobile" : width < 1024 ? "tablet" : "desktop";
  return {
    viewportWidth: width,
    viewportHeight: height,
    deviceClass,
    orientation: width > height ? "landscape" : "portrait",
    touch: window.matchMedia("(pointer: coarse)").matches || navigator.maxTouchPoints > 0,
    locale: navigator.language || "en",
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    prefersReducedMotion: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
    surface
  };
}
