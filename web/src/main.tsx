import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import { initPwa } from "./app/pwa";
import { applyTheme } from "./app/theme";
import "./index.css";

applyTheme();
initPwa();

const root = document.getElementById("root");
if (!root) throw new Error("#root missing");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
