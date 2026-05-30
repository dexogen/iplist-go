#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from ipaddress import ip_network
from pathlib import Path
from typing import Any


DATA_FIELDS = ("domains", "ip4", "ip6", "cidr4", "cidr6")
CUSTOM_ARRAY_FIELDS = (*DATA_FIELDS, "as")
REPLACE_FIELDS = ("cidr4", "cidr6")
DEFAULT_DNS = ["127.0.0.11:53", "77.88.8.88:53", "8.8.8.8:53", "1.1.1.1:53"]
DEFAULT_EXTERNAL = {field: [] for field in DATA_FIELDS}
DEFAULT_REPLACE = {"cidr4": {}, "cidr6": {}}


def default_site_config() -> dict[str, Any]:
    return {
        "domains": [],
        "dns": DEFAULT_DNS.copy(),
        "timeout": 0,
        "ip4": [],
        "ip6": [],
        "cidr4": [],
        "cidr6": [],
        "external": copy.deepcopy(DEFAULT_EXTERNAL),
        "replace": copy.deepcopy(DEFAULT_REPLACE),
    }


def read_json(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        data = json.load(handle)
    if not isinstance(data, dict):
        raise ValueError(f"{path}: root JSON value must be an object")
    return data


def write_json(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=4) + "\n", encoding="utf-8")


def normalize_strings(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return sorted({item.strip() for item in value if isinstance(item, str) and item.strip()})


def normalize_asns(value: Any) -> list[str]:
    output: set[str] = set()
    for item in normalize_strings(value):
        upper = item.upper()
        number = upper[2:] if upper.startswith("AS") else upper
        if not number.isdigit() or int(number) <= 0:
            raise ValueError(f"invalid AS value {item!r}")
        output.add("AS" + str(int(number)))
    return sorted(output, key=lambda item: int(item[2:]))


def merge_strings(base: Any, custom: Any) -> list[str]:
    return normalize_strings([*normalize_strings(base), *normalize_strings(custom)])


def normalize_replace_map(value: Any) -> dict[str, list[str]]:
    if not isinstance(value, dict):
        return {}
    output: dict[str, list[str]] = {}
    for key, replacement in value.items():
        if not isinstance(key, str) or not key.strip():
            continue
        output[key.strip()] = normalize_strings(replacement)
    return dict(sorted(output.items()))


def merge_replace(base: Any, custom: Any) -> dict[str, dict[str, list[str]]]:
    output: dict[str, dict[str, list[str]]] = {}
    base = base if isinstance(base, dict) else {}
    custom = custom if isinstance(custom, dict) else {}
    for field in REPLACE_FIELDS:
        merged = normalize_replace_map(base.get(field))
        merged.update(normalize_replace_map(custom.get(field)))
        output[field] = dict(sorted(merged.items()))
    return output


def merge_external(base: Any, custom: Any) -> dict[str, list[str]]:
    output: dict[str, list[str]] = {}
    base = base if isinstance(base, dict) else {}
    custom = custom if isinstance(custom, dict) else {}
    for field in DATA_FIELDS:
        output[field] = merge_strings(base.get(field), custom.get(field))
    return output


def merge_site_config(base: dict[str, Any] | None, custom: dict[str, Any]) -> dict[str, Any]:
    output = default_site_config()
    if base:
        output.update(copy.deepcopy(base))

    for field in DATA_FIELDS:
        output[field] = merge_strings(output.get(field), custom.get(field))
    if "as" in output or "as" in custom:
        output["as"] = normalize_asns([*normalize_asns(output.get("as")), *normalize_asns(custom.get("as"))])

    if "dns" in custom:
        output["dns"] = normalize_strings(custom.get("dns"))
    else:
        output["dns"] = normalize_strings(output.get("dns")) or DEFAULT_DNS.copy()

    if "timeout" in custom:
        timeout = custom["timeout"]
        if not isinstance(timeout, int) or timeout < 0:
            raise ValueError("timeout must be a non-negative integer")
        output["timeout"] = timeout
    elif not isinstance(output.get("timeout"), int) or output["timeout"] < 0:
        output["timeout"] = 0

    output["external"] = merge_external(output.get("external"), custom.get("external"))
    output["replace"] = merge_replace(output.get("replace"), custom.get("replace"))

    handled = {"dns", "timeout", "external", "replace", *CUSTOM_ARRAY_FIELDS}
    for key, value in custom.items():
        if key not in handled:
            output[key] = copy.deepcopy(value)

    return output


class RipeClient:
    def __init__(self, base_url: str, cache_root: Path | None, offline: bool) -> None:
        self.base_url = base_url.rstrip("/")
        self.cache_root = cache_root
        self.offline = offline

    def announced_prefixes(self, asn: str) -> list[str]:
        data = self._load(asn)
        prefixes = data.get("data", {}).get("prefixes", [])
        if not isinstance(prefixes, list):
            raise ValueError(f"RIPE response for {asn} does not contain data.prefixes")

        output: list[str] = []
        for entry in prefixes:
            if not isinstance(entry, dict) or not isinstance(entry.get("prefix"), str):
                continue
            prefix = entry["prefix"].strip()
            if not prefix:
                continue
            output.append(str(ip_network(prefix, strict=False)))
        return sorted(set(output))

    def _load(self, asn: str) -> dict[str, Any]:
        if not self.offline:
            try:
                data = self._fetch(asn)
                self._write_cache(asn, data)
                return data
            except (OSError, TimeoutError, urllib.error.URLError, json.JSONDecodeError) as error:
                cached = self._read_cache(asn)
                if cached is not None:
                    print(f"using cached RIPE response for {asn}: {error}", file=sys.stderr)
                    return cached
                raise RuntimeError(f"failed to fetch RIPE prefixes for {asn}: {error}") from error

        cached = self._read_cache(asn)
        if cached is None:
            raise RuntimeError(f"offline mode requires cached RIPE response for {asn}")
        return cached

    def _fetch(self, asn: str) -> dict[str, Any]:
        query = urllib.parse.urlencode({"resource": asn, "min_peers_seeing": "0"})
        request = urllib.request.Request(
            f"{self.base_url}/data/announced-prefixes/data.json?{query}",
            headers={"Accept": "application/json", "User-Agent": "iplist-go config merger"},
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            charset = response.headers.get_content_charset() or "utf-8"
            return json.loads(response.read().decode(charset))

    def _cache_path(self, asn: str) -> Path | None:
        if self.cache_root is None:
            return None
        return self.cache_root / f"{asn}.json"

    def _read_cache(self, asn: str) -> dict[str, Any] | None:
        path = self._cache_path(asn)
        if path is None or not path.is_file():
            return None
        return read_json(path)

    def _write_cache(self, asn: str, data: dict[str, Any]) -> None:
        path = self._cache_path(asn)
        if path is None:
            return
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def expand_as_prefixes(config: dict[str, Any], ripe: RipeClient) -> dict[str, Any]:
    asns = normalize_asns(config.get("as"))
    if not asns:
        return config

    cidr4 = normalize_strings(config.get("cidr4"))
    cidr6 = normalize_strings(config.get("cidr6"))
    for asn in asns:
        for prefix in ripe.announced_prefixes(asn):
            network = ip_network(prefix, strict=False)
            if network.version == 4:
                cidr4.append(str(network))
            else:
                cidr6.append(str(network))
    config["as"] = asns
    config["cidr4"] = normalize_strings(cidr4)
    config["cidr6"] = normalize_strings(cidr6)
    return config


def custom_config_files(custom_root: Path) -> list[Path]:
    if not custom_root.is_dir():
        return []
    paths: list[Path] = []
    for path in custom_root.rglob("*.json"):
        rel = path.relative_to(custom_root)
        if any(part.startswith(".") for part in rel.parts):
            continue
        if len(rel.parts) < 3:
            raise ValueError(f"{path}: custom configs must be stored as <set>/<group>/<site>.json")
        paths.append(path)
    return sorted(paths)


def merge_custom_configs(config_root: Path, custom_root: Path, ripe: RipeClient) -> int:
    changed = 0
    for custom_path in custom_config_files(custom_root):
        rel = custom_path.relative_to(custom_root)
        target_path = config_root.joinpath(*rel.parts)
        base = read_json(target_path) if target_path.exists() else None
        custom = read_json(custom_path)
        merged = merge_site_config(base, custom)
        merged = expand_as_prefixes(merged, ripe)
        write_json(target_path, merged)
        changed += 1
        print(f"merged custom config: {rel}", file=sys.stderr)
    return changed


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config-root", type=Path, required=True)
    parser.add_argument("--custom-root", type=Path, required=True)
    parser.add_argument("--ripe-base-url", default="https://stat.ripe.net")
    parser.add_argument("--ripe-cache-root", type=Path)
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()

    cache_root = args.ripe_cache_root
    if cache_root is None and args.custom_root.is_dir():
        cache_root = args.custom_root / ".cache" / "ripe"

    ripe = RipeClient(args.ripe_base_url, cache_root, args.offline)
    count = merge_custom_configs(args.config_root, args.custom_root, ripe)
    print(f"custom configs merged: {count}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
