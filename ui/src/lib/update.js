// Updating the CMS from the admin (Settings › Updates, the dashboard's
// notice): a backup and the new release staged on the server, which then
// restarts on it; this waits for it to answer as the new version, then
// reloads. onStatus hears what's happening, in words.
import { post } from "./api.js";
import { confirm } from "./confirm.svelte.js";

export async function updateTo(version, onStatus) {
  if (!(await confirm({ title: `Update to ${version}?`, message: "A backup is taken first (it stays in the data directory). The CMS then restarts on the new version: a few seconds offline. If the new version doesn't come up, it goes back to this one.", confirmLabel: "Update" }))) return false;
  onStatus("Taking a backup and getting the new version ready…");
  const r = await post("/tools/update", {});
  onStatus(`Restarting on ${r.updating_to}…`);
  const until = Date.now() + 120000;
  while (Date.now() < until) {
    await new Promise((ok) => setTimeout(ok, 2000));
    try {
      const s = await fetch("/api/session", { headers: { Accept: "application/json" } }).then((x) => x.json());
      if (s.version === r.updating_to || s.version === `v${r.updating_to}`) {
        location.reload();
        return true;
      }
    } catch {
      // still restarting
    }
  }
  onStatus("It's taking longer than expected: reload in a minute. If it's still the old version, Settings › Updates will say why.");
  return true;
}
