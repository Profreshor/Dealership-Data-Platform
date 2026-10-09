function rgb(hex: string) { return [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16)); }
function hex(values: number[]) { return `#${values.map((value) => Math.round(value).toString(16).padStart(2, "0")).join("")}`; }
function mix(first: string, second: string, amount: number) { const target = rgb(second); return hex(rgb(first).map((value, index) => value * (1 - amount) + target[index] * amount)); }
function luminance(color: string) {
  return rgb(color).map((value) => value / 255).map((value) => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4)
    .reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0);
}
function contrast(first: string, second: string) { const a = luminance(first), b = luminance(second); return (Math.max(a, b) + .05) / (Math.min(a, b) + .05); }

const dark = {
  background: "#0f1416", surface: "#141b1e", "surface-raised": "#1a2326", "surface-hover": "#1b2528",
  text: "#e9eff0", "text-strong": "#f3f7f7", muted: "#9aa9ad", line: "#2d3b40", "input-bg": "#0e1416", "input-border": "#647982",
  danger: "#ffada5", "danger-bg": "#321d1d", "danger-muted": "#eebcba", warning: "#f0d9a7", "warning-bg": "#42361f", success: "#bff2dd", "success-bg": "#173d32",
};
const light = {
  background: "#f7faf9", surface: "#ffffff", "surface-raised": "#f2f6f5", "surface-hover": "#e7f0ed",
  text: "#20302b", "text-strong": "#10221c", muted: "#536460", line: "#c9d5d2", "input-bg": "#ffffff", "input-border": "#788c85",
  danger: "#a52d29", "danger-bg": "#fde9e7", "danger-muted": "#7c2521", warning: "#815b00", "warning-bg": "#fff4d6", success: "#176b4d", "success-bg": "#dff5ea",
};

export function themeCSS(brand: string): string {
  if (!/^#[0-9a-f]{6}$/i.test(brand)) throw new Error("accent must be a six-digit hex color");
  const block = (mode: "dark" | "light") => {
    const base = mode === "dark" ? dark : light;
    const target = mode === "dark" ? "#ffffff" : "#000000";
    let accent = brand, soft = mix(accent, base.surface, .9);
    // Move only as far as needed for readable links, focus and selected items.
    for (let step = 0; step <= 100; step++) {
      accent = mix(brand, target, step / 100);
      soft = mix(accent, base.surface, .9);
      if ([base.background, base.surface, base["surface-hover"], soft].every((background) => contrast(accent, background) >= 4.5)) break;
    }
    const ink = contrast(accent, "#000000") > contrast(accent, "#ffffff") ? "#000000" : "#ffffff";
    const tokens = { ...base, accent, "accent-ink": ink, "accent-hover": mix(accent, target, .1), "accent-soft": soft, "accent-text": accent, "selection-bg": accent, "selection-ink": ink };
    return `:root {color-scheme:${mode};${Object.entries(tokens).map(([name, value]) => `--${name}:${value};`).join("")}}`;
  };
  return `${block("dark")}@media (prefers-color-scheme: light) {${block("light")}}`;
}
