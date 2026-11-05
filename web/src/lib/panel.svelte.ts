// The document panel beside the library table: how wide it is, whether it
// is expanded, and whether it is closed. The width is remembered in this
// browser; storage can be unavailable, so every read and write is guarded.

const key = "docsearch.panel.width";

/** The narrowest panel, and the least the table keeps beside it. */
export const minPanel = 360;
export const minTable = 360;
const fallback = 480;

function stored(): number {
  try {
    const w = Number(localStorage.getItem(key));
    return Number.isFinite(w) && w >= minPanel ? w : fallback;
  } catch {
    return fallback;
  }
}

/** A width kept between the panel's minimum and what leaves the table its own. */
export function clampWidth(width: number, room: number): number {
  return Math.round(Math.max(minPanel, Math.min(width, room - minTable)));
}

class Panel {
  width = $state(stored());
  closed = $state(false);
  /** The width to go back to when an expanded panel collapses. */
  #before = $state(0);

  get expanded(): boolean {
    return this.#before > 0;
  }

  resize(width: number, room: number): void {
    this.width = clampWidth(width, room);
    this.#before = 0;
  }

  /** Expands to the widest the table allows, or collapses back. */
  toggle(room: number): void {
    if (this.#before) {
      this.width = clampWidth(this.#before, room);
      this.#before = 0;
    } else {
      this.#before = this.width;
      this.width = clampWidth(room, room);
    }
    this.save();
  }

  save(): void {
    try {
      if (!this.#before) localStorage.setItem(key, String(this.width));
    } catch {
      // A private window or blocked storage: the width lasts this visit only.
    }
  }
}

export const panel = new Panel();
