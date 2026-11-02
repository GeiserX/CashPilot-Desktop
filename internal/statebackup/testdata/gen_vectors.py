"""Writes web_vectors.json: bundles sealed by the web app's own code.

The Go tests open these, so a change on either side that breaks the shared
format fails there. Regenerate with the web repository's app/state_backup.py:

    python3 -m venv venv && ./venv/bin/pip install cryptography
    ./venv/bin/python gen_vectors.py /path/to/CashPilot/app/state_backup.py > web_vectors.json

It also checks the other direction, a bundle sealed by Go
(go test ./internal/statebackup -run TestWriteGoVector -args -go-vector=go_bundle.json):

    ./venv/bin/python gen_vectors.py state_backup.py --open go_bundle.json
"""

import base64
import hashlib
import importlib.util
import io
import json
import sys
import tarfile


def load(path):
    spec = importlib.util.spec_from_file_location("state_backup", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def archive(entries):
    """One tar, shaped like Docker's archive of a single mount."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for name, kind, mode, data, link in entries:
            info = tarfile.TarInfo(name)
            info.mode = mode
            info.uid = info.gid = 1000
            info.mtime = 1767225600
            if kind == "dir":
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
            elif kind == "sym":
                info.type = tarfile.SYMTYPE
                info.linkname = link
                tar.addfile(info)
            else:
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def describe(archives):
    out = {}
    for top, data in sorted(archives.items()):
        rows = []
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as tar:
            for m in tar:
                body = tar.extractfile(m).read() if m.isfile() else b""
                rows.append(
                    {
                        "name": m.name,
                        "type": "dir" if m.isdir() else "sym" if m.issym() else "file",
                        "mode": m.mode,
                        "linkname": m.linkname,
                        "sha256": hashlib.sha256(body).hexdigest(),
                    }
                )
        out[top] = rows
    return out


def main():
    sb = load(sys.argv[1])
    if len(sys.argv) > 3 and sys.argv[2] == "--open":
        with open(sys.argv[3]) as fh:
            vec = json.load(fh)
        bundle = base64.b64decode(vec["bundle"])
        if vec.get("passphrase"):
            payload = sb.open_bundle(bundle, passphrase=vec["passphrase"])
        else:
            payload = sb.open_bundle(bundle, recipient_private_key=vec["private_key"])
        print(json.dumps({"header": sb.read_header(bundle), "archives": describe(sb.split_archives(payload))}, indent=1))
        return

    payload = archive(
        [
            ("mysterium-node", "dir", 0o700, None, None),
            ("mysterium-node/keystore", "dir", 0o700, None, None),
            ("mysterium-node/keystore/UTC--2026-key", "file", 0o600, b'{"address":"0xabc","crypto":{}}', None),
            ("mysterium-node/current", "sym", 0o777, None, "keystore/UTC--2026-key"),
        ]
    ) + archive(
        [
            ("identity", "dir", 0o755, None, None),
            ("identity/identity.key", "file", 0o4600, b"-----BEGIN PRIVATE KEY-----\nAAAA\n", None),
        ]
    )
    meta = {"slug": "mysterium", "worker": "vector-box", "targets": ["/app/identity", "/var/lib/mysterium-node"]}
    private, public = sb.generate_recipient_keypair()
    passphrase = "correct horse battery staple"
    by_pass = sb.seal(payload, passphrase=passphrase, metadata=meta)
    by_key = sb.seal(payload, recipient_public_key=public, metadata=meta)
    print(
        json.dumps(
            {
                "passphrase": passphrase,
                "private_key": private,
                "public_key": public,
                "payload_sha256": hashlib.sha256(payload).hexdigest(),
                "passphrase_bundle": base64.b64encode(by_pass).decode(),
                "recipient_bundle": base64.b64encode(by_key).decode(),
                "archives": describe(sb.split_archives(payload)),
            },
            indent=1,
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
