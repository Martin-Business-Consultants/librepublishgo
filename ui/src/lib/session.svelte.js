// Who's signed in, what they can do, and the site's name and branding.
import { brand } from "./branding.svelte.js";

class Session {
  me = $state(null);
  csrf = $state("");
  site = $state("");
  version = $state("");
  needsSetup = $state(false);
  ready = $state(false);

  can(capability) {
    return !!this.me && this.me.capabilities.includes(capability);
  }

  async load() {
    const { get } = await import("./api.js");
    try {
      const s = await get("/session", { quiet: true });
      this.site = s.site || "";
      brand.set(s.branding);
      this.version = s.version || "";
      this.needsSetup = !!s.needs_setup;
      this.me = s.signed_in ? s : null;
      this.csrf = s.signed_in ? s.csrf : "";
      await this.learnAddress();
    } catch {
      this.me = null;
    }
    this.ready = true;
  }

  // The CMS's own address (Settings › General), which image and file URLs
  // start with: unknown to the server until set, but not to the browser an
  // Administrator opens the admin in. Not on localhost: there the server's
  // own guess is right, and a dev server's port isn't the CMS's.
  async learnAddress() {
    if (!this.me?.system || this.me.cms_address_set !== false || ["localhost", "127.0.0.1", "[::1]"].includes(location.hostname)) return;
    const { patch } = await import("./api.js");
    try {
      await patch("/settings/general", { setting: { data: { cms_url: location.origin } } }, { quiet: true });
      this.me.cms_address_set = true;
    } catch {
      // Settings › General says it's unset, and offers this address.
    }
  }

  signedOut() {
    this.me = null;
    this.csrf = "";
  }
}

export const session = new Session();
