import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./App";

// apply persisted theme before first paint
try {
  const t = JSON.parse(localStorage.getItem("opsdeck.theme") ?? '"dark"');
  document.documentElement.classList.add(t === "light" ? "light" : "dark");
} catch {
  document.documentElement.classList.add("dark");
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
