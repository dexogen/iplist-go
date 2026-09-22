#!/usr/bin/env python3
"""Download a checksummed fallback snapshot for an application image."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import urllib.parse
import urllib.request


def fetch(url, limit):
    with urllib.request.urlopen(url, timeout=90) as response:
        body = response.read(limit + 1)
    if len(body) > limit:
        raise ValueError("snapshot exceeds size limit")
    return body


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    body = fetch(args.url, 4 << 20)
    manifest = json.loads(body)
    if manifest.get("schema_version") != 1 or not {"main", "beta", "russia"} <= manifest.get("sets", {}).keys():
        raise ValueError("invalid manifest")
    for key, desc in manifest["sets"].items():
        path = desc["path"]
        if not re.fullmatch(r"objects/[a-f0-9]{64}\.json\.gz", path) or desc["bytes"] > 512 << 20 or desc["unpacked_bytes"] > 512 << 20:
            raise ValueError("invalid snapshot descriptor")
        relative = path.rsplit("/", 1)[-1] if "/releases/download/" in args.url else path
        compressed = fetch(urllib.parse.urljoin(args.url, relative), desc["bytes"])
        if len(compressed) != desc["bytes"] or hashlib.sha256(compressed).hexdigest() != desc["sha256"]:
            raise ValueError("snapshot checksum mismatch")
        with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
            raw = stream.read(desc["unpacked_bytes"] + 1)
        if len(raw) != desc["unpacked_bytes"]:
            raise ValueError("expanded snapshot size mismatch")
        payload = json.loads(raw)
        if payload.get("config_set") != key or len(payload.get("sites", [])) != desc["sites"]:
            raise ValueError("snapshot content mismatch")
        target = args.output / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(compressed)
    (args.output / "manifest.json").write_bytes(body)


if __name__ == "__main__":
    main()
