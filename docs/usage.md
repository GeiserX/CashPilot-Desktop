# Usage

A visual tour of the app (**v0.20.9**), captured from the running desktop build.
The shots use sample data: five services running on the local Docker runtime, about
six weeks of made-up earnings history, and three example fleet devices. None of the
balances, credentials or devices are real. Where a screen displayed a
locally-generated API token, a LAN IP, a hostname or a home path, those values are
masked (`<API_KEY>`, `192.168.x.x`, `my-mac`, `/Users/you/`). The loopback
`127.0.0.1` is kept so the fleet/API addresses stay accurate.

## Onboarding

First launch welcomes you and then checks for a container runtime, with
copy-paste install guides for Docker, Colima, Lima, and Podman across macOS,
Linux, and Windows.

![Welcome](images/screenshots/01-onboarding.png)

![Runtime check and install guides](images/screenshots/01b-onboarding-runtime.png)

## Dashboard

The home view: total balance, today's and this month's earnings, and active
service count across the top; a daily-earnings chart with the latest collected
balance for each platform; and your deployed services with their status, uptime,
balance, CPU and memory.

![Dashboard](images/screenshots/02-dashboard.png)

## Service Catalog

Browse the built-in catalog of passive-income and DePIN providers (bandwidth,
DePIN, storage, and compute), each with its earnings model, requirements, and a
one-click path into the setup wizard.

![Service Catalog](images/screenshots/03-catalog.png)

## Setup Wizard

A guided flow — pick categories, choose providers, enter credentials, and deploy —
so you don't have to hand-write container configs.

![Setup Wizard](images/screenshots/06-wizard.png)

### A service's image

Each service runs from the image its catalog entry names, pinned by digest. To run
it from another one, expand **Advanced** on the service's card and type the image
in the **Image** box: a registry mirror, or a community build for an architecture
the provider does not publish.

- Blank means the catalog image. Clearing the box and deploying goes back to it.
- The choice is saved with the deployment, and every redeploy keeps it until you
  clear the box. Removing the service forgets it.
- Only the image changes. Capabilities, devices, ports, the entrypoint and the stop
  timeout still come from the catalog entry, so a custom image gets nothing the
  service does not already have. It does receive the service's credentials, so use
  an image you trust.
- The dashboard and the catalog mark such a deployment **custom image** and judge
  "update available" against the image you chose, not the catalog's.

## Settings

Configure the display currency, collection interval, data retention, and per-service
credentials. Credentials are encrypted at rest with a master key held in the OS
keychain.

![Settings](images/screenshots/04-settings.png)

## Fleet

Run CashPilot across more than one machine: point workers or a mobile companion at
the desktop's loopback API and they register automatically. Silent devices are
flipped offline and long-dead ones are reaped, so the list stays honest.

![Fleet Management](images/screenshots/05-fleet.png)
