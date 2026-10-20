-- The Rails core's tables, as db/schema.rb has them (only those the Go core
-- uses), then the Go core's own. JSON columns are text, as Rails keeps them
-- in SQLite.

CREATE TABLE IF NOT EXISTS roles (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  name varchar NOT NULL,
  description varchar,
  permissions json DEFAULT '[]' NOT NULL,
  system boolean DEFAULT 0 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  email varchar NOT NULL,
  name varchar NOT NULL,
  locale varchar,
  password_digest varchar NOT NULL,
  recovery_code_digests json DEFAULT '[]' NOT NULL,
  role_id integer REFERENCES roles(id),
  totp_enabled boolean DEFAULT 0 NOT NULL,
  totp_enabled_at datetime(6),
  totp_last_counter bigint,
  totp_secret varchar,
  verified boolean DEFAULT 0 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_users_on_email ON users (email);

CREATE TABLE IF NOT EXISTS api_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  user_id integer NOT NULL REFERENCES users(id),
  prefix varchar NOT NULL,
  token text,
  token_digest varchar NOT NULL,
  last_used_at datetime(6),
  last_used_ip varchar,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_api_tokens_on_token_digest ON api_tokens (token_digest);

CREATE TABLE IF NOT EXISTS service_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  name varchar NOT NULL,
  description text,
  role_id integer NOT NULL,
  created_by_id integer,
  prefix varchar NOT NULL,
  token text,
  token_digest varchar NOT NULL,
  last_used_at datetime(6),
  last_used_ip varchar,
  revoked_at datetime(6),
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_service_tokens_on_token_digest ON service_tokens (token_digest);

CREATE TABLE IF NOT EXISTS translation_groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  kind varchar NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS collections (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  slug varchar NOT NULL,
  name varchar NOT NULL,
  icon varchar,
  api_enabled boolean DEFAULT 0 NOT NULL,
  build_config json DEFAULT '{}' NOT NULL,
  enable_blocks boolean DEFAULT 0 NOT NULL,
  schema json DEFAULT '{}' NOT NULL,
  categories_collection_id integer,
  tags_collection_id integer,
  notification_emails text,
  notification_events json DEFAULT '[]' NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_collections_on_slug ON collections (slug);

CREATE TABLE IF NOT EXISTS collection_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  collection_id integer NOT NULL REFERENCES collections(id),
  slug varchar NOT NULL,
  title varchar NOT NULL,
  status varchar DEFAULT 'draft' NOT NULL,
  locale varchar DEFAULT 'en' NOT NULL,
  body_markdown text DEFAULT '' NOT NULL,
  blocks json DEFAULT '[]' NOT NULL,
  frontmatter json DEFAULT '{}' NOT NULL,
  seo json DEFAULT '{}' NOT NULL,
  category_entry_id integer,
  translation_group_id integer,
  lock_version integer DEFAULT 0 NOT NULL,
  publish_at datetime(6),
  published_at datetime(6),
  unpublish_at datetime(6),
  deleted_at datetime(6),
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE INDEX IF NOT EXISTS index_collection_entries_on_collection_id ON collection_entries (collection_id);

CREATE TABLE IF NOT EXISTS collection_entry_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  collection_entry_id integer NOT NULL,
  author_id integer,
  blocks json DEFAULT '[]' NOT NULL,
  body_markdown text DEFAULT '' NOT NULL,
  frontmatter json DEFAULT '{}' NOT NULL,
  comment varchar,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS pages (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  slug varchar NOT NULL,
  path varchar NOT NULL,
  parent_id integer,
  depth integer DEFAULT 0 NOT NULL,
  title varchar NOT NULL,
  status varchar DEFAULT 'draft' NOT NULL,
  locale varchar DEFAULT 'en' NOT NULL,
  blocks json DEFAULT '[]' NOT NULL,
  frontmatter json DEFAULT '{}' NOT NULL,
  schema json DEFAULT '{"fields":[]}' NOT NULL,
  seo json DEFAULT '{}' NOT NULL,
  category_entry_id integer,
  translation_group_id integer,
  lock_version integer DEFAULT 0 NOT NULL,
  publish_at datetime(6),
  published_at datetime(6),
  unpublish_at datetime(6),
  deleted_at datetime(6),
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE INDEX IF NOT EXISTS index_pages_on_path ON pages (path);

CREATE TABLE IF NOT EXISTS page_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  page_id integer NOT NULL,
  author_id integer,
  blocks json DEFAULT '[]' NOT NULL,
  comment varchar,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS taggings (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  tag_entry_id integer NOT NULL,
  taggable_id integer NOT NULL,
  taggable_type varchar NOT NULL,
  position integer DEFAULT 0 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS globals (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  slug varchar NOT NULL,
  name varchar NOT NULL,
  description varchar,
  icon varchar,
  data json DEFAULT '{}' NOT NULL,
  schema json DEFAULT '{}' NOT NULL,
  version integer DEFAULT 1 NOT NULL,
  lock_version integer DEFAULT 0 NOT NULL,
  deleted_at datetime(6),
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS global_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  global_id integer NOT NULL,
  author_id integer,
  data json DEFAULT '{}' NOT NULL,
  name varchar,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS block_types (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  slug varchar NOT NULL,
  label varchar NOT NULL,
  description varchar,
  category varchar,
  icon varchar,
  fields json DEFAULT '[]' NOT NULL,
  defaults json DEFAULT '{}' NOT NULL,
  built_in boolean DEFAULT 0 NOT NULL,
  deprecated boolean DEFAULT 0 NOT NULL,
  version integer DEFAULT 1 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_block_types_on_slug ON block_types (slug);

CREATE TABLE IF NOT EXISTS block_patterns (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  slug varchar NOT NULL,
  name varchar NOT NULL,
  description text,
  blocks json DEFAULT '[]' NOT NULL,
  synced boolean DEFAULT 0 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS assets (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  name varchar,
  alt varchar,
  caption text,
  description text,
  folder varchar DEFAULT '/' NOT NULL,
  focal_x float DEFAULT 0.5 NOT NULL,
  focal_y float DEFAULT 0.5 NOT NULL,
  deleted_at datetime(6),
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

-- Active Storage's, kept so a Rails install's storage/ directory serves as is.
CREATE TABLE IF NOT EXISTS active_storage_blobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  key varchar NOT NULL,
  filename varchar NOT NULL,
  content_type varchar,
  metadata text,
  service_name varchar NOT NULL,
  byte_size bigint NOT NULL,
  checksum varchar,
  created_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_active_storage_blobs_on_key ON active_storage_blobs (key);

CREATE TABLE IF NOT EXISTS active_storage_attachments (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  name varchar NOT NULL,
  record_type varchar NOT NULL,
  record_id bigint NOT NULL,
  blob_id bigint NOT NULL,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS content_references (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  owner_type varchar NOT NULL,
  owner_id integer NOT NULL,
  ref_type varchar NOT NULL,
  ref_id varchar NOT NULL,
  kind varchar NOT NULL,
  position integer,
  created_at datetime(6) NOT NULL
);
CREATE INDEX IF NOT EXISTS index_content_references_on_ref ON content_references (ref_type, ref_id);

CREATE TABLE IF NOT EXISTS redirects (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  source_path varchar NOT NULL,
  destination_url varchar NOT NULL,
  status_code integer DEFAULT 301 NOT NULL,
  active boolean DEFAULT 1 NOT NULL,
  wildcard boolean DEFAULT 0 NOT NULL,
  hit_count integer DEFAULT 0 NOT NULL,
  last_hit_at datetime(6),
  notes text,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  key varchar NOT NULL,
  data json DEFAULT '{}' NOT NULL,
  secrets text,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_settings_on_key ON settings (key);

CREATE TABLE IF NOT EXISTS audit_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  action varchar NOT NULL,
  actor_id bigint,
  actor_type varchar,
  actor_label varchar DEFAULT '' NOT NULL,
  target_id bigint,
  target_type varchar,
  target_label varchar DEFAULT '' NOT NULL,
  metadata json DEFAULT '{}' NOT NULL,
  ip varchar,
  user_agent varchar,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS webhooks (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  name varchar NOT NULL,
  url varchar NOT NULL,
  secret varchar NOT NULL,
  kind varchar DEFAULT 'internal' NOT NULL,
  body_map json DEFAULT '[]' NOT NULL,
  events json DEFAULT '[]' NOT NULL,
  event_filters json DEFAULT '{}' NOT NULL,
  headers json DEFAULT '{}' NOT NULL,
  active boolean DEFAULT 1 NOT NULL,
  failure_count integer DEFAULT 0 NOT NULL,
  last_delivery_at datetime(6),
  last_status varchar,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  webhook_id integer NOT NULL,
  event varchar NOT NULL,
  payload text NOT NULL,
  response_status integer,
  success boolean DEFAULT 0 NOT NULL,
  duration_ms integer,
  error text,
  created_at datetime(6) NOT NULL
);

CREATE TABLE IF NOT EXISTS device_authorizations (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  device_code varchar NOT NULL,
  user_code varchar NOT NULL,
  hostname varchar,
  label varchar,
  purpose varchar DEFAULT 'user' NOT NULL,
  user_id integer,
  approved_at datetime(6),
  denied_at datetime(6),
  expires_at datetime(6) NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

-- The Go core's own -------------------------------------------------------

-- A browser session for the admin, by the digest of its cookie.
CREATE TABLE IF NOT EXISTS admin_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  token_digest varchar NOT NULL UNIQUE,
  csrf varchar NOT NULL,
  user_id integer NOT NULL,
  ip varchar,
  user_agent varchar,
  last_seen_at datetime(6),
  created_at datetime(6) NOT NULL
);

-- A write to something live from a caller without the publish capability,
-- held for a person (202 pending_review).
CREATE TABLE IF NOT EXISTS revisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  record_type varchar NOT NULL,
  record_id integer NOT NULL,
  record_label varchar DEFAULT '' NOT NULL,
  changes json DEFAULT '{}' NOT NULL,
  fields json DEFAULT '[]' NOT NULL,
  status varchar DEFAULT 'open' NOT NULL,
  author_label varchar DEFAULT '' NOT NULL,
  reviewer_id integer,
  note text,
  decided_at datetime(6),
  created_at datetime(6) NOT NULL
);

-- Installed plugins (Go, compiled into the binary): what each asked for,
-- and what a person granted it.
CREATE TABLE IF NOT EXISTS plugins (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  key varchar NOT NULL UNIQUE,
  name varchar NOT NULL,
  version varchar NOT NULL,
  description text,
  manifest json DEFAULT '{}' NOT NULL,
  granted json DEFAULT '{}' NOT NULL,
  enabled boolean DEFAULT 0 NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL
);

-- Each plugin's own storage, namespaced by its key.
CREATE TABLE IF NOT EXISTS plugin_kv (
  plugin_key varchar NOT NULL,
  key varchar NOT NULL,
  value blob,
  updated_at datetime(6) NOT NULL,
  PRIMARY KEY (plugin_key, key)
);

-- One-time links mailed to a person: a password reset, or a new email
-- address to confirm. Only the digest is kept; a link works once, until it
-- expires.
CREATE TABLE IF NOT EXISTS account_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL,
  user_id integer NOT NULL,
  purpose varchar NOT NULL,
  token_digest varchar NOT NULL UNIQUE,
  new_email varchar,
  expires_at datetime(6) NOT NULL,
  used_at datetime(6),
  created_at datetime(6) NOT NULL
);
CREATE INDEX IF NOT EXISTS index_account_tokens_on_user ON account_tokens (user_id, purpose);

-- Who is editing what in the admin, kept fresh by the editor's heartbeat:
-- a lock nobody has refreshed for two minutes is free.
CREATE TABLE IF NOT EXISTS edit_locks (
  record_type varchar NOT NULL,
  record_id integer NOT NULL,
  user_id integer NOT NULL,
  user_name varchar NOT NULL,
  session_id integer,
  acquired_at datetime(6) NOT NULL,
  heartbeat_at datetime(6) NOT NULL,
  PRIMARY KEY (record_type, record_id)
);

-- What an editor had typed and not saved, per person and record ("new"
-- for one not created yet), to offer back after a crash or a takeover.
CREATE TABLE IF NOT EXISTS autosaves (
  record_type varchar NOT NULL,
  record_key varchar NOT NULL,
  user_id integer NOT NULL,
  data json NOT NULL,
  created_at datetime(6) NOT NULL,
  updated_at datetime(6) NOT NULL,
  PRIMARY KEY (record_type, record_key, user_id)
);

-- Each person's admin preferences: list columns, rows per page, density,
-- the publish checklist.
CREATE TABLE IF NOT EXISTS user_preferences (
  user_id integer PRIMARY KEY NOT NULL,
  data json DEFAULT '{}' NOT NULL,
  updated_at datetime(6) NOT NULL
);
