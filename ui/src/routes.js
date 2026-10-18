// Every screen, by address. The first pattern that matches wins.
import Dashboard from "./routes/Dashboard.svelte";
import Pages from "./routes/Pages.svelte";
import PageEdit from "./routes/PageEdit.svelte";
import Collections from "./routes/Collections.svelte";
import CollectionEdit from "./routes/CollectionEdit.svelte";
import Entries from "./routes/Entries.svelte";
import EntryEdit from "./routes/EntryEdit.svelte";
import Globals from "./routes/Globals.svelte";
import GlobalEdit from "./routes/GlobalEdit.svelte";
import Media from "./routes/Media.svelte";
import BlockTypes from "./routes/BlockTypes.svelte";
import BlockTypeEdit from "./routes/BlockTypeEdit.svelte";
import Revisions from "./routes/Revisions.svelte";
import RevisionShow from "./routes/RevisionShow.svelte";
import Redirects from "./routes/Redirects.svelte";
import Webhooks from "./routes/Webhooks.svelte";
import Trash from "./routes/Trash.svelte";
import Audit from "./routes/Audit.svelte";
import Users from "./routes/Users.svelte";
import Roles from "./routes/Roles.svelte";
import Settings from "./routes/Settings.svelte";
import SettingsDeploy from "./routes/SettingsDeploy.svelte";
import SettingsTokens from "./routes/SettingsTokens.svelte";
import SettingsBrand from "./routes/SettingsBrand.svelte";
import SettingsUpdates from "./routes/SettingsUpdates.svelte";
import Plugins from "./routes/Plugins.svelte";
import PluginScreen from "./routes/PluginScreen.svelte";
import Account from "./routes/Account.svelte";
import Connect from "./routes/Connect.svelte";
import Backup from "./routes/Backup.svelte";
import Import from "./routes/Import.svelte";

export const routes = [
  ["/", Dashboard],
  ["/pages", Pages],
  ["/pages/new", PageEdit],
  ["/pages/edit/*rest", PageEdit],
  ["/collections", Collections],
  ["/collections/new", CollectionEdit],
  ["/collections/:slug/settings", CollectionEdit],
  ["/collections/:slug/new", EntryEdit],
  ["/collections/:slug/edit/:entry", EntryEdit],
  ["/collections/:slug", Entries],
  ["/globals", Globals],
  ["/globals/:slug", GlobalEdit],
  ["/media", Media],
  ["/blocks", BlockTypes],
  ["/blocks/:slug", BlockTypeEdit],
  ["/revisions", Revisions],
  ["/revisions/:id", RevisionShow],
  ["/redirects", Redirects],
  ["/webhooks", Webhooks],
  ["/trash", Trash],
  ["/audit", Audit],
  ["/users", Users],
  ["/roles", Roles],
  ["/settings", Settings],
  ["/settings/deploy", SettingsDeploy],
  ["/settings/tokens", SettingsTokens],
  ["/settings/brand", SettingsBrand],
  ["/settings/updates", SettingsUpdates],
  ["/settings/:section", Settings],
  ["/plugins", Plugins],
  ["/plugins/:key/:screen", PluginScreen],
  ["/account", Account],
  ["/connect", Connect],
  ["/backup", Backup],
  ["/import", Import],
];
