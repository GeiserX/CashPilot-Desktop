# Moving from CashFactory

CashPilot Desktop can read CashFactory's `.env` and bring your service logins
over, so you do not type them again. You check everything before CashPilot
saves it. The CashPilot web app has the same import, with the same rules but
one: a value with spaces at either end is trimmed there, as its deploy form
trims what you type, and marked to retype here.

## Before you start

Stop CashFactory on this computer before you deploy the same services from
CashPilot. Two copies behind one connection earn as one, and the same EarnApp
device id must not run twice. Run `docker compose down` in the CashFactory
folder. Its
[compose file](https://github.com/OlivierGaland/CashFactory/blob/0ff4bd0c3b020d82c91e095db43ba5c9e356ac94/docker-compose.yml)
also starts Portainer and two Watchtower containers that mount the Docker
socket (lines 31, 141 and 264). Make sure they are gone too: `docker ps` should
list none of them.

## Which file to use

Use the `.env` file in your CashFactory folder, the one you filled in from the
[sample](https://github.com/OlivierGaland/CashFactory/blob/0ff4bd0c3b020d82c91e095db43ba5c9e356ac94/.env).

Docker Compose reads that file, and it expands `$` in values that are not in
single quotes. A value like that is not what the file shows, so CashPilot asks
you to retype it rather than guess.

## How to import

1. Open **Settings**, then **Import** under **Import from another tool**.
2. Add the `.env` file, or paste its text. Leave the tool on "Tell from the
   files", or pick CashFactory.
3. Press **Check what comes over**. Tick the services you want and fill in any
   value marked missing.
4. Press **Save the ticked services**, then **Deploy** each one from the same
   page, or later from the catalog: the deploy form opens with the saved values.

## What comes over

| CashFactory app | Keys read | CashPilot service | What comes over |
|---|---|---|---|
| `earnapp` | `EARNAPP_DEVICE_ID` | EarnApp | device id, so your linked device keeps earning |
| `honeygain` | `HONEYGAIN_EMAIL`, `HONEYGAIN_PASSWD` | Honeygain | email, password |
| `iproyalpawns` | `IPROYALPAWNS_EMAIL`, `IPROYALPAWNS_PASSWD` | IPRoyal Pawns | email, password |
| `packetstream` | `PACKETSTREAM_CID` | PacketStream | CID |
| `traffmonetizer` | `TRAFFMONETIZER_TOKEN` | Traffmonetizer | token |
| `repocket` | `RP_EMAIL`, `RP_API_KEY` | Repocket | email, API key |
| `proxylite` | `PROXYLITE_USER_ID` | ProxyLite | user id |
| `proxyrack` | `PROXYRACK_HOST_UUID` | ProxyRack | device UUID |
| `earnfm` | `EARNFM_APIKEY` | Earn.fm | API key |
| `peer2profit` | `PEER2PROFIT_EMAIL` | Peer2Profit | nothing: no longer available |

A value still showing the sample's placeholder (`<email>`, `sdk-node-<md5sum>`)
counts as not set, and a service with nothing but placeholders is left out of
the table.

## What never comes over

- **`DEVICE_NAME`.** CashPilot names the device itself. Your old devices stay
  listed as offline in each provider's dashboard until you remove them there.
- **Bitping.** CashFactory keeps the Bitping login in a Docker volume, not in
  `.env`, so the import has nothing to read and shows no Bitping row. Type the
  login in Bitping's deploy form.
- **Portainer and Watchtower.** CashPilot manages and updates its own
  containers.

## A service you already set up in CashPilot

If a service already has values saved in CashPilot, the import keeps them and
only fills fields that are empty. The page tells you which values it left as
they were.

If a value saved here differs from the file and the import would also fill an
empty field of the same service, the import stops and saves nothing, so one
service never ends up with half of two logins. Fix that service in Settings
first, then import again.

## Earnings tracking

CashPilot keeps one set of values per service, read by both the container and
the earnings tracking. For Honeygain and IPRoyal Pawns the container login is
also the account login, so tracking works as soon as you save. Other services
need an earnings login the container never uses; add it in the deploy form, or
from **Settings > Earnings Collection**.

## Safety

- Nothing in the file is run or expanded, and no address in it is fetched.
- The file is read once and not kept. What you save is stored encrypted, like
  every other credential in CashPilot.
- Secrets are shown only as "set" on the page.
