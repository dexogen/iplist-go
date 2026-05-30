#!/usr/bin/env python3

from __future__ import annotations

import argparse
import concurrent.futures
import json
import re
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


FALLBACK_ICON = "generic.svg"
GENERIC_SVG = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="Service">
  <title>Service</title>
  <rect width="64" height="64" rx="14" fill="#2f343b"/>
  <path d="M16 21.5A7.5 7.5 0 0 1 23.5 14h17A7.5 7.5 0 0 1 48 21.5v21A7.5 7.5 0 0 1 40.5 50h-17A7.5 7.5 0 0 1 16 42.5v-21Z" fill="#f5f7fa" opacity=".92"/>
  <path d="M22 25h20M22 32h20M22 39h13" stroke="#2f343b" stroke-width="4" stroke-linecap="round"/>
</svg>
"""
BRAND_ALIASES = {
    "adobe": ["adobe.com"],
    "amazon": ["amazon.com"],
    "apple": ["apple.com"],
    "google": ["google.com"],
    "kick": ["kick.com"],
    "twitch": ["twitch.tv"],
}


def configured_sites(config_root: Path) -> list[str]:
    sites: set[str] = set()
    for path in config_root.rglob("*.json"):
        if len(path.relative_to(config_root).parts) >= 3:
            sites.add(path.stem)
    return sorted(sites)


def icon_candidates(site: str) -> list[str]:
    candidates = [site]
    if "@" in site:
        brand, target = site.split("@", 1)
        candidates.extend([target, *parent_domains(target), brand, f"{brand}.com"])
        candidates.extend(BRAND_ALIASES.get(brand, []))
    else:
        candidates.extend(parent_domains(site))
    return list(dict.fromkeys(candidate.lower() for candidate in candidates if candidate))


def parent_domains(value: str) -> list[str]:
    if "." not in value:
        return []
    parts = value.split(".")
    return [".".join(parts[index:]) for index in range(1, len(parts) - 1)]


def safe_filename(site: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_.@-]+", "_", site) + ".png"


def favicon_url(domain: str) -> str:
    query = urllib.parse.urlencode({"domain": domain, "sz": "64"})
    return f"https://www.google.com/s2/favicons?{query}"


def fetch_icon(site: str, icons_dir: Path, timeout: float) -> tuple[str, str]:
    filename = safe_filename(site)
    path = icons_dir / filename
    for candidate in icon_candidates(site):
        req = urllib.request.Request(
            favicon_url(candidate),
            headers={"User-Agent": "iplist-go icon builder"},
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as response:
                content_type = response.headers.get("Content-Type", "")
                body = response.read(128 * 1024)
        except (OSError, urllib.error.URLError):
            continue
        if response.status != 200 or "image" not in content_type or len(body) < 64:
            continue
        path.write_bytes(body)
        return site, filename
    return site, FALLBACK_ICON


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo-root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--timeout", type=float, default=5.0)
    parser.add_argument("--workers", type=int, default=8)
    args = parser.parse_args()

    repo = args.repo_root
    config_root = repo / "config"
    icons_dir = repo / "storage" / "icons"
    icons_dir.mkdir(parents=True, exist_ok=True)
    (icons_dir / FALLBACK_ICON).write_text(GENERIC_SVG)

    sites = configured_sites(config_root)
    mapping: dict[str, str] = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.workers)) as executor:
        futures = [executor.submit(fetch_icon, site, icons_dir, args.timeout) for site in sites]
        for future in concurrent.futures.as_completed(futures):
            site, icon = future.result()
            mapping[site] = icon

    storage_path = repo / "storage" / "icons.json"
    ordered = {key: mapping[key] for key in sorted(mapping)}
    storage_path.write_text(json.dumps(ordered, ensure_ascii=False, indent=2) + "\n")

    resolved = sum(1 for icon in ordered.values() if icon != FALLBACK_ICON)
    print(f"icons mapped: {len(ordered)}")
    print(f"downloaded icons: {resolved}")
    print(f"neutral fallback icons: {len(ordered) - resolved}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
