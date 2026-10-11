CREATE TABLE songs (
 song_id INTEGER PRIMARY KEY CHECK(song_id>0),
 name TEXT, artist TEXT, dancer TEXT, player_count INTEGER, volume REAL,
 start REAL, end REAL,
 flip INTEGER CHECK(flip IN (0,1)), double_width INTEGER CHECK(double_width IN (0,1)),
 skip_random INTEGER CHECK(skip_random IN (0,1)), disable_public INTEGER CHECK(disable_public IN (0,1)),
 rpe INTEGER, genre TEXT, group_name TEXT, composed_title TEXT, composed_title_spell TEXT, aya_id TEXT,
 tags_json TEXT CHECK(json_valid(tags_json) AND json_type(tags_json)='array'),
 original_urls_json TEXT CHECK(json_valid(original_urls_json) AND json_type(original_urls_json)='array'),
 shader_motion_json TEXT CHECK(json_valid(shader_motion_json) AND json_type(shader_motion_json)='array')
);
CREATE TABLE media (
 md5 TEXT PRIMARY KEY NOT NULL CHECK(length(md5)=32 AND md5 NOT GLOB '*[^0-9a-f]*'),
 byte_size INTEGER CHECK(byte_size>=0), source_path TEXT
);
CREATE TABLE song_media (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 md5 TEXT NOT NULL REFERENCES media(md5)
);
CREATE INDEX song_media_md5 ON song_media(md5);
CREATE TABLE song_urls (
 song_id INTEGER NOT NULL CHECK(song_id>0),
 api TEXT NOT NULL, node TEXT NOT NULL, url TEXT NOT NULL,
 md5 TEXT NOT NULL CHECK(length(md5)=32), byte_size INTEGER NOT NULL CHECK(byte_size>0),
 query_started_at INTEGER NOT NULL, observed_at INTEGER NOT NULL,
 failed_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(song_id, api, node)
);
CREATE TABLE song_usage (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 demand_count INTEGER NOT NULL CHECK(demand_count>=0),
 demand_score REAL NOT NULL CHECK(demand_score>=0),
 last_demand_at INTEGER NOT NULL
);
CREATE TABLE song_playback (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 transfer_count INTEGER NOT NULL, last_transfer_at INTEGER NOT NULL
);
CREATE TABLE playback_latency (
 cache_result TEXT PRIMARY KEY, samples INTEGER NOT NULL, first_body_ns INTEGER NOT NULL
);
CREATE TABLE catalog_state (
 catalog_key TEXT PRIMARY KEY, revision TEXT NOT NULL, digest TEXT NOT NULL,
 source TEXT NOT NULL, accepted_at INTEGER NOT NULL
);
CREATE TABLE catalog_checks (
 catalog_key TEXT PRIMARY KEY, checked_at INTEGER NOT NULL DEFAULT 0,
 message TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE catalog_members (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id)
);
CREATE TABLE media_access (
 md5 TEXT PRIMARY KEY NOT NULL,
 last_requested_at INTEGER NOT NULL
);
CREATE TABLE request_events (
 event_id INTEGER PRIMARY KEY,
 resource_key TEXT NOT NULL,
 requested_at INTEGER NOT NULL,
 version_key TEXT NOT NULL,
 host TEXT NOT NULL,
 source TEXT NOT NULL,
 method TEXT NOT NULL,
 range_header TEXT NOT NULL,
 cache_result TEXT NOT NULL,
 outcome TEXT NOT NULL,
 file_bytes INTEGER NOT NULL,
 transferred_bytes INTEGER NOT NULL,
 elapsed_ms INTEGER NOT NULL,
 status INTEGER NOT NULL,
 counts_as_demand INTEGER NOT NULL
);
CREATE INDEX request_events_resource_time ON request_events(resource_key, requested_at);
CREATE INDEX request_events_time ON request_events(requested_at);CREATE INDEX request_events_http_time
 ON request_events(requested_at DESC, event_id DESC) WHERE source='http';
CREATE TABLE traffic_totals (
 id INTEGER PRIMARY KEY CHECK(id=1), saved_bytes INTEGER NOT NULL,
 hits INTEGER NOT NULL, misses INTEGER NOT NULL
);