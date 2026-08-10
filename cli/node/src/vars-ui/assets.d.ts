// The stylesheet is an input to the bundler, not a module with a value: the
// import exists so esbuild emits app.css beside app.js.
declare module "*.css";
