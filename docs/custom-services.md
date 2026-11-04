# Custom services

A custom service is an image you add yourself, from Docker Hub or any other registry, for a
service the catalog does not list yet. CashPilot Desktop deploys it on this computer, shows it
on the dashboard, and lets you stop, start, read the logs of and remove it like any other
service. You do not edit any YAML.

CashPilot has not reviewed a custom image. It runs with exactly the restrictions every catalog
service runs with, and CashPilot cannot read its earnings. The CashPilot web app has the same
feature, with the same rules.

## Adding one

Open **Catalog**. Above the list, under **Your custom services**, choose **Add custom service**,
then fill in:

| Field | What it takes |
|---|---|
| **Name** | What the dashboard calls it, up to 60 characters. Its internal name, `custom-<name>`, never changes after you create it, so renaming moves nothing. A second service with the same name becomes `custom-<name>-v2`, and a name ending in a number loses the dash before it (`Node 2` is `custom-node2`), because `-2` on a container name means a second copy. A deleted service's internal name is never given out again. |
| **Image** | `publisher/image:tag`, a full registry path such as `ghcr.io/publisher/image:1.4.2`, or a digest such as `publisher/image@sha256:...`. An image with no tag or the `latest` tag is accepted with a warning, because every redeploy then pulls whatever the publisher pushed last. |
| **Environment variables** | Up to 50 name and value pairs, stored encrypted like every credential. Tick **Secret** for a token or password: its value is never shown again, and leaving it blank when you edit keeps the stored one. |
| **Command** | Optional. One line, passed to the image as its command. `${NAME}` is replaced by the value of variable `NAME`. It is split into arguments, never run through a shell. |
| **Data path** | Optional. A path inside the container, such as `/data`. CashPilot keeps it in a Docker volume of its own, `cashpilot-custom-<name>-data`, so it survives redeploys. The container itself is `cashpilot-custom-<name>`. Blank means nothing is kept. |
| **Memory limit** | From `32m` to `16g`. Defaults to `512m`. |

Then choose **Deploy** on it. A service with a data path runs one copy; one without can run
several.

## What it is allowed to do

The same as a catalog service with no special needs, and nothing more:

- every Linux capability dropped, and none added back;
- `no-new-privileges`, a process limit and the memory limit above;
- never privileged, never on the host network, no host devices;
- no host folders: its only mount is its own named volume;
- no published ports;
- Docker's default bridge network, like every service Desktop runs.

The definition has no field for any of those grants, so nothing typed in the form can add one.
No catalog entry may use the `custom-` prefix either, so a catalog update can never take over a
custom service. As with catalog services, an image whose main process is an init system such as
systemd is refused before the running container is touched.

These limits mean an image that needs a published port, a capability like `NET_ADMIN`, a device
such as `/dev/net/tun`, or host networking will not work as a custom service. Such a service
belongs in the catalog, where those grants are reviewed: open an issue or a pull request on
https://github.com/GeiserX/CashPilot

## What it does not do

- **No earnings.** CashPilot has no collector for it, so the dashboard shows a dash for its
  balance. Check its earnings on the provider's own site.
- **No compose export.** Its variables stay encrypted in the app's database.
- **Redeploys use the saved definition.** The image and variables are changed by editing the
  service, never in the deploy form. The dashboard shows **update available** when the running
  image differs from the definition; deploy it again to apply the change.

## Removing it

1. On the dashboard, choose **Remove** on it. You are asked separately whether to delete its data
   volume too; saying no keeps the data for a later deploy.
2. Then, under **Catalog**, **Your custom services**, choose **Delete**. CashPilot refuses while
   the service is still deployed here. A data volume you kept in step 1 stays; a new custom
   service never gets the same internal name, so it never mounts it. Delete it by hand when you
   no longer need it: `docker volume rm cashpilot-custom-<name>-data`.
