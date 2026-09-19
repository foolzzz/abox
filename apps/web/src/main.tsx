import React from "react";
import ReactDOM from "react-dom/client";
import { registerSW } from "virtual:pwa-register";
import { App } from "./App";
import { LocaleProvider } from "./lib/i18n";
import "./styles.css";

if ("serviceWorker" in navigator) {
  const hadController = Boolean(navigator.serviceWorker.controller);
  let reloadStarted = false;

  navigator.serviceWorker.addEventListener("controllerchange", () => {
    if (!hadController || reloadStarted) return;
    reloadStarted = true;
    window.location.reload();
  });

  const updateSW = registerSW({
    immediate: true,
    onNeedRefresh() {
      void updateSW(true);
    },
    onRegisteredSW(_serviceWorkerUrl, registration) {
      void registration?.update();
    }
  });
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <LocaleProvider><App /></LocaleProvider>
  </React.StrictMode>
);
