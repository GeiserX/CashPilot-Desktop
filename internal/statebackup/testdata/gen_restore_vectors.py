"""Writes web_restore_vectors.json: what the web app's own restore code says about one backup.

The Go restore tests (manifest_test.go) read it, so the two apps keep judging a
restore the same way: the bundle opens in both, the folder manifests match, and
the differences come out as the same lines. Regenerate from a checkout of the
web repository, with its own environment:

    cd /path/to/CashPilot
    uv run python /path/to/CashPilot-Desktop/internal/statebackup/testdata/gen_restore_vectors.py \
        app/state_backup.py > /path/to/CashPilot-Desktop/internal/statebackup/testdata/web_restore_vectors.json

The passphrase is a fixed test string, not a secret.
"""

import base64
import importlib.util
import io
import json
import sys
import tarfile

PASSPHRASE = "restore vector passphrase, not a secret"
# Over 100 bytes, so it travels as a PAX path record, as a real keystore file does.
KEY_NAME = "UTC--2026-01-01T00-00-00.000000000Z--" + "0123456789abcdef" * 5


def load(path):
    spec = importlib.util.spec_from_file_location("state_backup", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def archive(entries):
    """One tar, shaped like Docker's archive of a single mount."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for name, kind, mode, uid, data, link in entries:
            info = tarfile.TarInfo(name)
            info.mode = mode
            info.uid = info.gid = uid
            info.mtime = 1767225600
            if kind == "dir":
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
            elif kind == "sym":
                info.type = tarfile.SYMTYPE
                info.linkname = link
                tar.addfile(info)
            elif kind == "hard":
                info.type = tarfile.LNKTYPE
                info.linkname = link
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def rows(manifest):
    return [[path, *entry] for path, entry in sorted(manifest.items())]


def main():
    sb = load(sys.argv[1])
    key = b'{"address":"0x00000000000000000000000000000000000000aa","crypto":{"cipher":"test"}}'
    payload = archive(
        [
            ("mysterium-node", "dir", 0o700, 0, None, None),
            ("mysterium-node/keystore", "dir", 0o700, 0, None, None),
            ("mysterium-node/keystore/" + KEY_NAME, "file", 0o600, 0, key, None),
            ("mysterium-node/keystore/remember.json", "file", 0o4644, 1000, b'{"identity":"0xaa"}', None),
            ("mysterium-node/keystore/copy.json", "hard", 0o4644, 1000, None, "mysterium-node/keystore/remember.json"),
            ("mysterium-node/current", "sym", 0o777, 0, None, "keystore/" + KEY_NAME),
        ]
    )
    meta = {"slug": "mysterium", "worker": "vector-box", "targets": ["/var/lib/mysterium-node"]}
    bundle = sb.seal(payload, passphrase=PASSPHRASE, metadata=meta)
    archives = sb.split_archives(sb.open_bundle(bundle, passphrase=PASSPHRASE))
    manifests = {top: sb.tree_manifest(data) for top, data in archives.items()}
    # The web writes through relative_archive; its manifest must be the backup's.
    relative = {top: sb.tree_manifest(sb.relative_archive(data, "volume")) for top, data in archives.items()}
    assert relative == manifests, (relative, manifests)

    base = manifests["mysterium-node"]
    cases = []

    def case(name, found):
        cases.append({"name": name, "found": rows(found), "lines": sb.manifest_differences(base, found)})

    case("identical", dict(base))
    changed = dict(base)
    path = "keystore/" + KEY_NAME
    changed[path] = (*base[path][:4], "0" * 64)
    case("changed file", changed)
    missing = {k: v for k, v in base.items() if k != "keystore/remember.json"}
    case("missing file", missing)
    extra = dict(base)
    extra["keystore/extra.json"] = ("file", 0o644, 0, 0, "1" * 64)
    extra["image-folder"] = ("dir", 0o755, 0, 0, "")
    case("extra file and image folder", extra)
    owner = dict(base)
    owner["."] = ("dir", 0o755, 0, 0, "")
    case("root owner", owner)
    kind = dict(base)
    kind["current"] = ("file", 0o777, 0, 0, "2" * 64)
    case("kind", kind)

    print(
        json.dumps(
            {
                "passphrase": PASSPHRASE,
                "bundle": base64.b64encode(bundle).decode(),
                "manifests": {top: rows(m) for top, m in sorted(manifests.items())},
                "cases": cases,
            },
            indent=1,
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
