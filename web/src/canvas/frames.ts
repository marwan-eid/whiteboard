/**
 * Renders on demand instead of every frame: anything that changes what is on
 * screen calls request(), and one animation frame runs the registered
 * pre-render steps and then renders once. An idle board costs nothing.
 */
export class FrameScheduler {
  private requested = false;
  private readonly before: (() => void)[] = [];
  /** Durations of recent frames (pre-render steps plus render), in ms. */
  readonly recent: number[] = [];

  constructor(
    private readonly render: () => void,
    private readonly raf: (cb: () => void) => void = (cb) => requestAnimationFrame(cb),
  ) {}

  /** Runs fn right before each render (e.g. to redraw a dirty overlay). */
  beforeRender(fn: () => void): void {
    this.before.push(fn);
  }

  request = (): void => {
    if (this.requested) return;
    this.requested = true;
    this.raf(() => {
      this.requested = false;
      const start = performance.now();
      for (const fn of this.before) fn();
      this.render();
      this.recent.push(performance.now() - start);
      if (this.recent.length > 2000) this.recent.shift();
    });
  };
}
