import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { themeCSS } from "./branding-tokens";

function contrast(hex: string, other: string) { const rgb = (value: string) => [0, 2, 4].map((i) => Number.parseInt(value.slice(i + 1, i + 3), 16) / 255).map((v) => v <= .03928 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4); const lum = (value: string) => rgb(value).reduce((s, v, i) => s + v * [0.2126, .7152, .0722][i], 0); const [a, b] = [lum(hex), lum(other)].sort((x, y) => y - x); return (a + .05) / (b + .05); }

describe("branding tokens", () => {
  it.each(["#67c7aa", "#2457c5", "#ffffff", "#000000", "#ffff00", "#ff0000"])("derives readable accent foregrounds for %s", (accent) => {
    const css = themeCSS(accent); const blocks = [...css.matchAll(/\{([^{}]+)\}/g)].map((match) => Object.fromEntries([...match[1].matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})/g)].map((item) => [item[1], item[2]])));
    expect(blocks).toHaveLength(2); expect(css).toContain("prefers-color-scheme: light");
    for (const tokens of blocks) {
      for (const [foreground, background] of [["accent","background"],["accent","surface"],["accent-text","accent-soft"],["accent-ink","accent"],["accent-ink","accent-hover"],["text","surface"],["text","background"],["muted","surface"],["danger","danger-bg"],["warning","warning-bg"],["success","success-bg"]]) {
        expect(contrast(tokens[foreground],tokens[background]),`${foreground}/${background}`).toBeGreaterThanOrEqual(4.5);
      }
      expect(contrast(tokens["input-border"],tokens["input-bg"])).toBeGreaterThanOrEqual(3);
      for (const file of ["./src/styles.css", "../../packages/ddp-ui/src/system-console.css"]) {
        const source=readFileSync(new URL(file,import.meta.url),"utf8");
        for (const [,name] of source.matchAll(/var\(--([\w-]+)\)/g)) expect(tokens[name],`missing ${name}`).toMatch(/^#[0-9a-f]{6}$/);
      }
    }
  });
  it("rejects non six-digit accents", () => expect(() => themeCSS("red")).toThrow());
});
