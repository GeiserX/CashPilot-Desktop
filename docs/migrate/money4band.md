# Moving from money4band

CashPilot Desktop can read money4band's config and bring your service logins
over, so you do not type them again. You check everything before CashPilot
saves it. The CashPilot web app has the same import, with the same rules but
one: a value with spaces at either end is trimmed there, as its deploy form
trims what you type, and marked to retype here.

## Before you start

Stop money4band on this computer before you deploy the same services from
CashPilot. Two copies behind one connection earn as one, and the same EarnApp
or ProxyRack device id must not run twice. Use **Stop apps stack** in
money4band's menu
([m4b-config.json](https://github.com/MRColorR/money4band/blob/3bfafb1d9a738aac11f2049d6f2c3515ff783bb8/config/m4b-config.json)),
or run `docker compose down` in its folder. If you set up extra proxy
instances, stop each of them too.

## Which file to use

Use `config/user-config.json` in your money4band folder. money4band writes it
when you run **Setup Apps**, and it holds exactly what you typed. The `.env`
next to it is generated from that file and also works, as does the hand-edited
`.env` of money4band 3.x.

Prefer the JSON file. In a `.env`, Docker Compose expands `$` in unquoted
values, so a password with `$` in it is not what the file shows. CashPilot does
not guess: it asks you to retype that value.

## How to import

1. Open **Settings**, then **Import** under **Import from another tool**.
2. Add `user-config.json`, or paste its text. Leave the tool on "Tell from the
   files", or pick money4band.
3. Press **Check what comes over**. Tick the services you want and fill in any
   value marked missing.
4. Press **Save the ticked services**, then **Deploy** each one from the same
   page, or later from the catalog: the deploy form opens with the saved values.

## What comes over

Field names come from money4band's
[user-config template](https://github.com/MRColorR/money4band/blob/3bfafb1d9a738aac11f2049d6f2c3515ff783bb8/template/user-config.json)
and the `.env` names its
[generator](https://github.com/MRColorR/money4band/blob/3bfafb1d9a738aac11f2049d6f2c3515ff783bb8/utils/generator.py#L535-L545)
writes (`<APP>_<FLAG>`, for example `REPOCKET_APIKEY`).

| money4band app | CashPilot service | What comes over |
|---|---|---|
| `earnapp` | EarnApp | device id (`uuid`), so your linked device keeps earning |
| `honeygain` | Honeygain | email, password |
| `iproyalpawns` | IPRoyal Pawns | email, password |
| `packetstream` | PacketStream | CID |
| `traffmonetizer` | Traffmonetizer | token |
| `repocket` | Repocket | email, API key |
| `earnfm` | Earn.fm | API key |
| `proxyrack` | ProxyRack | API key, device UUID |
| `proxylite` | ProxyLite | user id |
| `bitping` | Bitping | email, password |
| `mystnode` | Mysterium | turned on; see the Mysterium note below |

An app you turned off in money4band shows up unticked. An app that is off and
holds nothing but the template's placeholders (`yourHONEYGAINMail`) and port
lists is left out, so an untouched `user-config.json` gives an empty table.

These apps are listed but not brought over:

| money4band app | Why |
|---|---|
| `proxybase` | money4band runs ProxyBase's old client with a user id. CashPilot runs the current one, which needs the Access Token from proxybase.org. |
| `urnetwork` | money4band keeps a session token. CashPilot signs URnetwork in with your email and password. |
| `grass` | Grass runs as a browser extension. CashPilot only tracks it, with the access token from the Grass dashboard. |
| `gradient`, `dawn`, `teneo`, `titan`, `spide` | CashPilot tracks these, but there is no container to deploy. |
| `wipter`, `packetshare`, `peer2profit`, `speedshare` | No longer available. |

Any other app is listed as "CashPilot has no such service".

## What never comes over

- **Device name.** CashPilot names the device itself. Your old devices stay
  listed as offline in each provider's dashboard until you remove them there.
- **Proxies.** Add each one in the Proxies section of the Fleet page, then run
  copies of your services through it; adding a proxy starts nothing by itself.
  Saving an import starts nothing either. A proxied copy needs the same service
  deployed on this computer first, and deploying it runs it on this computer's
  own connection, so a service that must never use that connection should stay
  unticked.
- **Watchtower and notifications.** CashPilot updates its own containers.
- **money4band's dashboard and its ports.**

## A service you already set up in CashPilot

If a service already has values saved in CashPilot, the import keeps them and
only fills fields that are empty. The page tells you which values it left as
they were. Change them in the deploy form if you want the imported ones.

If a value saved here differs from the file and the import would also fill an
empty field of the same service, the import stops and saves nothing, so one
service never ends up with half of two logins. Fix that service in Settings
first, then import again.

## Earnings tracking

CashPilot keeps one set of values per service, read by both the container and
the earnings tracking. For Honeygain, IPRoyal Pawns and Bitping the container
login is also the account login, so tracking works as soon as you save. Other
services need an earnings login the container never uses (a Repocket account
password, an EarnApp browser cookie); add it in the deploy form, or from
**Settings > Earnings Collection**.

## Mysterium

Importing turns Mysterium on, but CashPilot starts a new node with a new
identity. The old node's keystore and any unsettled earnings stay in
money4band's `.data/mysterium-node` folder
([app-config.json](https://github.com/MRColorR/money4band/blob/3bfafb1d9a738aac11f2049d6f2c3515ff783bb8/config/app-config.json)).
Settle them first.

A 3.x `.env` cannot tell whether you ran Mysterium: its template always
lists the dashboard port. From a 3.x file Mysterium is not imported, so add
it from the catalog if you ran it.

## Safety

- Nothing in the file is run or expanded, and no address in it is fetched.
- The file is read once and not kept. What you save is stored encrypted, like
  every other credential in CashPilot.
- Secrets are shown only as "set" on the page.
