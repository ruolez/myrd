// xterm.js writes some of its CSS into <style> elements it creates (the
// scrollbar, and the DOM renderer's cell metrics and colors). The app's CSP
// (style-src 'self') blocks inline styles, so those rules never apply.
// Constructed stylesheets are not inline styles: this mirrors every <style>
// under the terminal into one, adopted by the document, and keeps it in
// sync as xterm rewrites them (theme and font changes).

/** Mirrors the <style> elements under root; returns the cleanup. */
export function adoptXtermStyles(root: HTMLElement): () => void {
  if (typeof CSSStyleSheet === "undefined" || !("adoptedStyleSheets" in document)) return () => {};
  const sheet = new CSSStyleSheet();
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  let last = "";
  const sync = () => {
    const css = [...root.querySelectorAll("style")].map((s) => s.textContent ?? "").join("\n");
    if (css === last) return;
    last = css;
    try {
      sheet.replaceSync(css);
    } catch {
      // a rule this browser rejects: keep the previous ones
    }
  };
  sync();
  const obs = new MutationObserver(sync);
  obs.observe(root, { subtree: true, childList: true, characterData: true });
  return () => {
    obs.disconnect();
    document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => s !== sheet);
  };
}
