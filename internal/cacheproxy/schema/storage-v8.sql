CREATE TABLE songs (
 id INTEGER PRIMARY KEY CHECK(id>0),
 name TEXT, artist TEXT, dancer TEXT, playerCount INTEGER, volume REAL,
 start REAL, end REAL,
 flip INTEGER CHECK(flip IN (0,1)), double_width INTEGER CHECK(double_width IN (0,1)),
 skip_random INTEGER CHECK(skip_random IN (0,1)), disable_public INTEGER CHECK(disable_public IN (0,1)),
 rpe INTEGER, major TEXT, groupName TEXT,
 tag TEXT CHECK(json_valid(tag) AND json_type(tag)='array'),
 shader_motion TEXT CHECK(json_valid(shader_motion) AND json_type(shader_motion)='array')
) STRICT;
CREATE TABLE media (
 md5 TEXT PRIMARY KEY NOT NULL CHECK(length(md5)=32 AND md5 NOT GLOB '*[^0-9a-f]*'),
 byte_size INTEGER CHECK(byte_size>=0)
) STRICT;
CREATE TABLE song_media (
 song_id INTEGER PRIMARY KEY REFERENCES songs(id),
 md5 TEXT NOT NULL REFERENCES media(md5)
) STRICT;
CREATE INDEX song_media_md5 ON song_media(md5);
CREATE TABLE song_preferences (
 song_id INTEGER PRIMARY KEY REFERENCES songs(id),
 policy TEXT NOT NULL CHECK(policy IN ('blocked','favorite'))
) STRICT;
CREATE TABLE song_urls (
 song_id INTEGER NOT NULL CHECK(song_id>0) REFERENCES songs(id) DEFERRABLE INITIALLY DEFERRED,
 api TEXT NOT NULL, node TEXT NOT NULL, url TEXT NOT NULL,
 md5 TEXT NOT NULL CHECK(length(md5)=32 AND md5 NOT GLOB '*[^0-9a-f]*') REFERENCES media(md5) DEFERRABLE INITIALLY DEFERRED,
 byte_size INTEGER NOT NULL CHECK(byte_size>0),
 query_started_at INTEGER NOT NULL, observed_at INTEGER NOT NULL,
 failed_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(song_id, api, node)
) STRICT;
CREATE TABLE song_usage (
 song_id INTEGER PRIMARY KEY REFERENCES songs(id),
 demand_count INTEGER NOT NULL CHECK(demand_count>=0),
 demand_score REAL NOT NULL CHECK(demand_score>=0),
 last_demand_at INTEGER NOT NULL
) STRICT;
CREATE TABLE song_playback (
 song_id INTEGER PRIMARY KEY REFERENCES songs(id),
 transfer_count INTEGER NOT NULL CHECK(transfer_count>=0), last_transfer_at INTEGER NOT NULL
) STRICT;
CREATE TABLE catalog_state (
 catalog_key TEXT PRIMARY KEY NOT NULL, revision TEXT NOT NULL, digest TEXT NOT NULL,
 source TEXT NOT NULL, accepted_at INTEGER NOT NULL
) STRICT;
CREATE TABLE catalog_checks (
 catalog_key TEXT PRIMARY KEY NOT NULL, checked_at INTEGER NOT NULL DEFAULT 0,
 message TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE TABLE catalog_members (
 song_id INTEGER PRIMARY KEY REFERENCES songs(id)
) STRICT;
CREATE TABLE media_access (
 md5 TEXT PRIMARY KEY NOT NULL CHECK(length(md5)=32 AND md5 NOT GLOB '*[^0-9a-f]*'),
 last_requested_at INTEGER NOT NULL
) STRICT;
CREATE TABLE request_events (
 event_id INTEGER PRIMARY KEY,
 resource_md5 TEXT CHECK(length(resource_md5)=32 AND resource_md5 NOT GLOB '*[^0-9a-f]*'),
 requested_at INTEGER NOT NULL,
 host TEXT NOT NULL,
 method TEXT NOT NULL,
 range_header TEXT NOT NULL,
 cache_result TEXT NOT NULL,
 outcome TEXT NOT NULL,
 file_bytes INTEGER NOT NULL CHECK(file_bytes>=0),
 transferred_bytes INTEGER NOT NULL CHECK(transferred_bytes>=0),
 elapsed_ms INTEGER NOT NULL CHECK(elapsed_ms>=0),
 status INTEGER NOT NULL,
 song_id INTEGER CHECK(song_id>0),
 completed_at INTEGER,
 response_bytes INTEGER CHECK(response_bytes>=0),
 first_body_ns INTEGER CHECK(first_body_ns>=0),
 first_body_blocking INTEGER CHECK(first_body_blocking IN (0,1)),
 download_bytes INTEGER CHECK(download_bytes>=0),
 cache_bytes INTEGER CHECK(cache_bytes>=0),
 local_bytes INTEGER CHECK(local_bytes>=0),
 CHECK((download_bytes IS NULL AND cache_bytes IS NULL AND local_bytes IS NULL) OR
       (download_bytes IS NOT NULL AND cache_bytes IS NOT NULL AND local_bytes IS NOT NULL))
) STRICT;
CREATE INDEX request_events_resource_time ON request_events(resource_md5, requested_at);
CREATE INDEX request_events_time ON request_events(requested_at DESC, event_id DESC);
CREATE TABLE request_observation (
 id INTEGER PRIMARY KEY CHECK(id=1), started_at INTEGER NOT NULL, retained_since INTEGER NOT NULL
) STRICT;
CREATE TABLE traffic_totals (
 id INTEGER PRIMARY KEY CHECK(id=1), saved_bytes INTEGER NOT NULL CHECK(saved_bytes>=0),
 hits INTEGER NOT NULL CHECK(hits>=0), misses INTEGER NOT NULL CHECK(misses>=0),
 download_bytes INTEGER NOT NULL DEFAULT 0 CHECK(download_bytes>=0),
 cache_bytes INTEGER NOT NULL DEFAULT 0 CHECK(cache_bytes>=0),
 local_bytes INTEGER NOT NULL DEFAULT 0 CHECK(local_bytes>=0)
) STRICT;
