// The Emscripten module scripts/build-audio-wasm.sh builds. TypeScript reads
// this instead of the generated audio.js, which it can't check.
export default function createModule(options?: object): Promise<any>;
