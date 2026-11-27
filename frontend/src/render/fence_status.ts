// The LAN fence in one line, for the dashboard and for Settings › LAN isolation.
//
// Settings let a person pick auto, on or off, but nothing said whether the fence is
// in place now or how many earners sit behind it. This line reads the status the
// fence check keeps (internal/runtime/lanfence.go): the setting, whether the fence
// was applied at the last check, and the running earners on the fenced network.
//
// Pure: takes the FenceStatus the backend sent, returns HTML, touches no global.

import type { FenceStatus } from "../wails";
import { escapeHtml } from "./format.js"; // .js so the emitted ESM resolves in Node; Vite maps it back to .ts
import { ht, t } from "./i18n.js";
import { runtimeState, type RuntimeInput } from "./runtime_state.js";

type EngineReading = Pick<RuntimeInput, "available" | "reason">;

/** The fence's state in plain words, without the setting. */
export function fenceStatusText(fence: FenceStatus, runtime: EngineReading): string {
  const { kind, label } = runtimeState(runtime);
  if (kind === "unknown") return t("fence_status.unknown");
  const inside = (fence.inside || []).length;
  const outside = (fence.outside || []).length;
  const outsideNote = outside ? ` ${t("fence_status.outside", { n: outside })}` : "";
  switch (fence.mode) {
    case "on":
      if (fence.state === "failed") return t("fence_status.on.failed");
      if (fence.state !== "applied") return t("fence_status.notChecked");
      if (!inside && !outside) return t("fence_status.on.empty");
      return `${t("fence_status.on.inPlace", { n: inside })}${outsideNote}`;
    case "legacy":
      return outside
        ? t("fence_status.legacy.outside", { n: outside })
        : t("fence_status.legacy.none");
    case "off":
      return t("fence_status.off");
    case "unsupported":
      return t("fence_status.unsupported");
  }
  // A cached fence check can say n/a even after Docker has started.
  if (fence.state === "failed") return t("fence_status.failed");
  if (fence.state === "n/a" && kind !== "ready") {
    const reason = kind === "not-running" ? t("fence_status.dockerNotRunning") : label;
    return t("fence_status.noEarner", { reason });
  }
  return t("fence_status.notChecked");
}

/**
 * One line: the setting, then the fence's state. "" when there is no Docker
 * runtime. tag is "p" on the dashboard and "small" inside a Settings row, whose
 * label may hold only inline content.
 */
export function renderFenceStatusLine(fence: FenceStatus | null | undefined, runtime: EngineReading, tag: "p" | "small" = "p"): string {
  if (!fence) return "";
  return `<${tag} class="muted compact-copy" data-fence-status="${escapeHtml(fence.mode || "unknown")}">${ht("fence_status.line", { setting: fence.setting || "auto" })} ${escapeHtml(fenceStatusText(fence, runtime))}</${tag}>`;
}
