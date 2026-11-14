# Backing up irreplaceable state

Some services keep their whole identity on this machine's disk: the Mysterium
keystore, the Storj node identity, the Anyone relay keys, a Sia host wallet. The
catalog marks those folders as `critical_volumes`. There is no copy anywhere else,
so a wiped disk or a new machine loses the node and whatever balance it held.

CashPilot refuses to delete those volumes when a service is removed. It cannot
protect them from a dead disk, so the dashboard asks you to keep a copy.

## The Back these up card

Under **Needs attention**, every deployed service with such a folder gets a line:
the folder, what it holds, and two buttons.

- **I have a backup** is for state you already copy with your own tools.
- **Download backup** reads the folder, encrypts it, and saves a `.cpbackup` file
  where you choose. Either a passphrase (12 characters or more, typed twice) or an
  X25519 public key. Only the catalog's critical folders are read, and the
  container is paused for the moment the read takes, so a keystore is never copied
  half written. Anything over 256 MB is refused: that is a storage folder (Storj,
  Sia), which needs a disk-level backup.

Either one marks the service as backed up, with the date. When every service is
marked, the card folds into one quiet line. **Undo** takes a confirmation back.

A confirmation lapses, and the card asks again, when:

- **the volume changed.** CashPilot keeps a fingerprint of where the state lives:
  the named volume and its creation time, or the host folder of a bind mount.
  Removing the volume and making a new one, or pointing the service somewhere
  else, changes it. Redeploying over the same volume (every image update) does not.
- **six months have passed.** The fingerprint cannot see a new identity generated
  inside the same volume, so the time limit covers that.

CashPilot keeps nothing that could open the file. The passphrase is used once and
dropped; with a public key no secret enters the app at all. Lose the passphrase or
the private key and the backup is gone, by design. There is no upload and no
scheduled backup: a timer would need a stored key.

The file format is the CashPilot web app's, so either app's tool opens either
app's backups.

## Restore a backup

**1. Open the file** on a machine you trust (needs Go and git). Run `cpbackup` from
a checkout of the release that made the backup:

```bash
git clone --branch <version> https://github.com/GeiserX/CashPilot-Desktop
cd CashPilot-Desktop
go run ./cmd/cpbackup decrypt <file>.cpbackup --out restored
```

It asks for the passphrase or private key at the prompt, never on the command
line. It checks the file, refuses an altered one, and writes one tar per folder
into an empty `--out` folder, printing where each one goes. Step 2 extracts as
root, so it also refuses the whole file if any entry could land outside its
folder (an absolute path, `..`, a link pointing out, a device or pipe), names a
path twice, is a sparse file, or carries an extended attribute such as a file
capability. The error says which rule fired. It writes each entry again from its
name, type, permission bits, owner ids, time and contents only, and drops setuid,
setgid and sticky bits. A file that unpacks to more than 256 MB or holds more
than 20,000 entries is refused too; no export is that large.

```
mysterium from my-mac, checksum verified.
  restored/mysterium-node.tar  ->  restores /var/lib/mysterium-node
```

The web app's `python -m app.state_backup decrypt` does the same.

**2. Put it back into an empty volume, before the service starts.** A service that
started without its state has already made a new identity. For the example above:

```bash
docker volume create mysterium-data
docker run --rm -i --network none -v mysterium-data:/var/lib/mysterium-node \
  alpine:3.22 tar -x -C /var/lib < restored/mysterium-node.tar
```

The volume name and path are the ones in the service's catalog entry
(`docker.volumes`). If the service already runs with a new identity, remove it in
CashPilot first, keeping its volumes, and move that volume's contents aside.

**3. Deploy the service** from CashPilot. It finds the restored volume and starts
with the old identity.

To make a key pair for public-key mode, run `go run ./cmd/cpbackup keygen` in that checkout. Keep the private key
offline and give CashPilot only the public one.
