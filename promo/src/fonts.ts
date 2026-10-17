import { continueRender, delayRender, staticFile } from "remotion";

// The UIs' fonts, local: Inter (vendored here) and the full JetBrains Mono
// (copied from the web UI's vendor directory by scripts/promo).
const faces = [
  new FontFace("Inter", `url(${staticFile("fonts/Inter-Variable.woff2")})`, { weight: "100 900" }),
  new FontFace("JetBrains Mono", `url(${staticFile("fonts/JetBrainsMono-Regular.woff2")})`, { weight: "400" }),
  new FontFace("JetBrains Mono", `url(${staticFile("fonts/JetBrainsMono-Bold.woff2")})`, { weight: "700" }),
];
const handle = delayRender("fonts");
Promise.all(faces.map((f) => f.load().then(() => document.fonts.add(f))))
  .then(() => continueRender(handle))
  .catch((err) => { console.error(err); continueRender(handle); });
