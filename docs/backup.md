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

## Restore from the app

**Settings > Restore from a backup** puts a node identity or wallet back on this
machine from a `.cpbackup` file. It covers Mysterium, ProxyBase and Anyone, whose
identity sits on a named Docker volume. Choose the file, read what the restore
would create, type the passphrase and confirm. The rules:

- **It only creates.** This machine must not have the service in any state (no
  container, no copy, nothing set aside), and its identity volume must not exist.
  The restore never writes over anything. To replace a running identity, download
  its backup first, remove the service and say yes to deleting its data, then
  restore.
- **Passphrase files only.** A file sealed to a key is refused before anything
  secret is asked for: its private key never goes through the app. Use the
  command line steps below for it.
- **Files the app did not make need the name typed.** The app keeps the SHA-256 of
  every file it exports. For any other file you type the service name, because
  nobody can tell from the file whose identity is inside. A file exported before
  this record existed is one of these.
- **One identity, one machine.** For a file the app did not make, the restore is
  refused while the machine named inside the file still runs the service, as far
  as this app's fleet list shows. For any file, the preview names the fleet
  machines that run the service now. A file the app made needs the name typed
  while one of them runs it: the record says the app wrote the file, not where its
  identity went since. When the app cannot read its fleet list, the restore is
  refused. Two machines on one identity look like one node to the provider, which
  may merge or ban them.
- **Checked before it starts.** The app creates the volumes, each with a random
  `cashpilot.restore` label, and the container without starting it. It writes the
  backup into the volume, reads the folder back and compares every file, link,
  owner and permission with the backup. Only then does the container start.
  Mysterium starts only behind the LAN fence; until the fence holds it waits, and
  the fence guard never starts it before its data is checked. On any failure the
  container and the volumes the restore made are removed. A volume under the same
  name without the restore's label belongs to another program and stays.
- **A restore that did not finish holds the service.** Before it makes anything,
  the app records that a restore is under way. The record goes once the data reads
  back as the backup, or once the undo removed everything. If the app stops
  mid-restore, or the undo cannot remove something (also after a start that
  failed), the record stays: Deploy,
  Start and Restore refuse the service, and the Restore card lists it. **Delete
  the partial restore** removes its container and the volumes the restore made,
  then the service is free again.
- **Nothing kept.** The app reads the file where it is and keeps only its path and
  SHA-256 for 15 minutes; the restore reads it again and refuses a file that
  changed. The passphrase is used once and never stored or logged. The service's
  saved settings are used, so fill them in on its deploy form first.

Storj and Sia keep their state in host folders, and Storj's is over the export
cap: restore those with the command line steps.

## Restore a backup from the command line

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
path twice, is a sparse file, carries an extended attribute such as a file
capability, has a control character in a name or link target, or has an invalid
time. The error says which rule fired. It writes each entry again from its name,
type, permission bits, owner ids, time and contents only, and drops setuid,
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
