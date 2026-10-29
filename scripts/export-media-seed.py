"""Read a local SQLite snapshot; export only resource identities, sizes and mappings.

No runtime schema migration. The explicit projection works with storage v7/v8.
The output contains no URLs, file paths, usage, preferences or machine identity.
"""
import argparse
import json
import re
import sqlite3
from pathlib import Path


def export(database, output, version):
    connection = sqlite3.connect(database.resolve().as_uri() + "?mode=ro", uri=True)
    try:
        connection.execute("PRAGMA query_only=ON")
        connection.execute("BEGIN")
        media = dict(connection.execute("SELECT md5,byte_size FROM media"))
        mappings = dict(connection.execute("SELECT song_id,md5 FROM song_media"))
        # Older stores could let catalogs overwrite mappings. Prefer the last
        # actual API observation across all API/node pairs in this one snapshot.
        mappings.update(connection.execute("""
            WITH latest AS (
              SELECT song_id,md5,ROW_NUMBER() OVER (
                PARTITION BY song_id ORDER BY query_started_at DESC,observed_at DESC,api,node
              ) AS n FROM song_urls
            ) SELECT song_id,md5 FROM latest WHERE n=1
        """))
        for md5, size in media.items():
            if not re.fullmatch(r"[0-9a-f]{32}", md5) or (size is not None and size <= 0):
                raise ValueError("invalid reference resource")
        if any(song_id <= 0 or md5 not in media for song_id, md5 in mappings.items()):
            raise ValueError("invalid reference mapping")
        snapshot = {
            "version": version,
            "sources": [],
            "media": [dict(md5=md5, **({"byteSize": size} if size is not None else {}))
                      for md5, size in sorted(media.items())],
            "songMedia": [dict(id=song_id, md5=md5) for song_id, md5 in sorted(mappings.items())],
        }
        output.write_text(json.dumps(snapshot, indent=2) + "\n", encoding="utf-8", newline="\n")
        print(f"Exported {len(media)} resources, {sum(size is not None for size in media.values())} sizes, {len(mappings)} mappings")
    finally:
        connection.rollback()
        connection.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("database", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--version", required=True, help="snapshot capture version YYYYMMDDhhmmss")
    args = parser.parse_args()
    export(args.database, args.output, args.version)
