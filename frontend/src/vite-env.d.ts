// Vite resolves CSS/asset side-effect imports at build time.
// Declare the modules so `tsc` type-checks cleanly (fixes TS2882 on `import "./style.css"`).
declare module "*.css";
declare module "*.svg";
declare module "*.png";

// Compile-time constant injected by Vite (see vite.config.ts) from package.json.
declare const __APP_VERSION__: string;
