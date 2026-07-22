import React from "react";
import { createRoot } from "react-dom/client";
import { Theme } from "@radix-ui/themes";
import "@radix-ui/themes/styles.css";
import "./styles/shared.css";
import "./styles/companion.css";
import "./styles/control-panel.css";
// SurfaceApp is the sole production router for this standalone Wails v3 app.
// It binds directly to CoreService; do not replace it with the retired
// fairy/desktop adapter path.
import { SurfaceApp } from "./surface.jsx";

createRoot(document.querySelector("#root")).render(
  <React.StrictMode>
    <Theme appearance="light" accentColor="sky" grayColor="slate" radius="large" scaling="100%" hasBackground={false}>
      <SurfaceApp />
    </Theme>
  </React.StrictMode>,
);
