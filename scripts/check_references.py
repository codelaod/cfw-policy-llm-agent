#!/usr/bin/env python3
"""Verify documentation reference files without network or third-party packages."""
from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path
from urllib.parse import urlparse


def verify(root: Path) -> int:
    manifests = sorted((root / "docs" / "references").glob("**/manifest.json"))
    if not manifests:
        raise ValueError("no reference manifests found")
    count = 0
    for manifest in manifests:
        data = json.loads(manifest.read_text(encoding="utf-8"))
        if data.get("schema_version") != 1 or not data.get("documents"):
            raise ValueError(f"unsupported or empty manifest: {manifest}")
        seen: set[str] = set()
        for item in data["documents"]:
            identifier = item["id"]
            if identifier in seen:
                raise ValueError(f"duplicate reference ID: {identifier}")
            seen.add(identifier)
            url = urlparse(item["url"])
            if url.scheme != "https" or not url.netloc:
                raise ValueError(f"invalid source URL: {identifier}")
            path = (manifest.parent / item["text_file"]).resolve()
            if not path.is_relative_to(manifest.parent.resolve()):
                raise ValueError(f"reference escapes its manifest directory: {identifier}")
            body = path.read_bytes()
            if len(body) != item["text_bytes"]:
                raise ValueError(f"size mismatch: {identifier}")
            if hashlib.sha256(body).hexdigest() != item["text_sha256"]:
                raise ValueError(f"SHA-256 mismatch: {identifier}")
            text = body.decode("utf-8")
            if item["url"] not in text or item["retrieved_date"] not in text:
                raise ValueError(f"missing source/date in snapshot: {identifier}")
            count += 1
    return count


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    try:
        count = verify(root)
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    print(f"PASS: {count} reference files in working tree; sources, sizes and SHA-256 verified (Git publication is checked separately).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
