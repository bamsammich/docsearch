class Toast {
  message = $state("");
  #timer: ReturnType<typeof setTimeout> | undefined;

  show(message: string): void {
    this.message = message;
    clearTimeout(this.#timer);
    this.#timer = setTimeout(() => {
      this.message = "";
    }, 4000);
  }
}

export const toast = new Toast();
