import { describe, expect, it } from "vitest";
import { clampWidth, minPanel, minTable } from "./panel.svelte";

describe("clampWidth", () => {
  it("keeps a width that fits", () => {
    expect(clampWidth(500, 1200)).toBe(500);
  });

  it("never goes below the panel's minimum", () => {
    expect(clampWidth(100, 1200)).toBe(minPanel);
  });

  it("leaves the table its own width", () => {
    expect(clampWidth(5000, 1200)).toBe(1200 - minTable);
  });

  it("holds the panel's minimum when the room is too small for both", () => {
    expect(clampWidth(500, 500)).toBe(minPanel);
  });
});
