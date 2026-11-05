import { mount } from "svelte";
import App from "./App.svelte";
// Fonts ship with the app rather than from a font CDN, so the page makes no
// third-party request and its CSP stays at 'self'.
import "@fontsource/inter/latin-400.css";
import "@fontsource/inter/latin-500.css";
import "@fontsource/inter/latin-600.css";
import "@fontsource/inter/latin-700.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "./app.css";

const target = document.getElementById("app");
if (!target) throw new Error("index.html has no #app element");

export default mount(App, { target });
