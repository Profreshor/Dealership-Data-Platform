import { describe, expect, it, vi } from "vitest";

describe("custom page registry", () => {
  it("registers and resolves pages by stable ID", async () => {
    vi.resetModules();
    const { getPage, registerPage } = await import("./pages");
    const Page = () => null;
    registerPage("dashboard", Page);
    expect(getPage("dashboard")).toBe(Page);
    expect(getPage("missing")).toBeUndefined();
  });

  it("rejects empty and duplicate IDs", async () => {
    vi.resetModules();
    const { registerPage } = await import("./pages");
    const Page = () => null;
    expect(() => registerPage("  ", Page)).toThrow("must not be empty");
    registerPage("dashboard", Page);
    expect(() => registerPage("dashboard", Page)).toThrow("already registered");
  });
});
