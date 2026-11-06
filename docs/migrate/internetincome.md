# Moving from InternetIncome

CashPilot Desktop can read InternetIncome's `properties.conf`, and the files
where it keeps your EarnApp and ProxyRack device ids, and bring them over. You
check everything before CashPilot saves it. The CashPilot web app has the same
import, with the same rules but one: a value with spaces at either end is
trimmed there, as its deploy form trims what you type, and marked to retype
here.

## Before you start

InternetIncome runs on Linux. Copy its files to the computer running CashPilot
Desktop, or paste their text.

Stop InternetIncome before you deploy the same services from CashPilot behind
the same connection. Two copies behind one connection earn as one, and the same
device id must not run twice. In the InternetIncome folder, run:

```bash
sudo bash internetIncome.sh --delete
```

`--delete` removes the containers and keeps `earnapp.txt`, `proxyrack.txt` and
the Mysterium data folder. `--deleteBackup` removes those as well
([internetIncome.sh, lines 61 to 64](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/internetIncome.sh#L61-L64)
and [line 1562](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/internetIncome.sh#L1562)),
so import first.

## Which files to use

From the InternetIncome folder:

- `properties.conf`, your settings.
- `earnapp.txt`, if you ran EarnApp. Each line holds a device link
  ([lines 1250 to 1261](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/internetIncome.sh#L1250-L1261)).
- `proxyrack.txt`, if you ran ProxyRack. One device UUID per line
  ([lines 817 to 827](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/internetIncome.sh#L817-L827)).

CashPilot takes the first device in each file: the one your main container
used and the one you linked to your account. The other lines belonged to proxy
copies, and the page tells you how many it left out.

InternetIncome runs every value through the shell
([line 1375](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/internetIncome.sh#L1375)).
A value in single quotes is taken as written, as the comments in
[properties.conf](https://github.com/engageub/InternetIncome/blob/db9338ab5a77539db8420476742af647b6cf82e5/properties.conf)
advise. A value outside single quotes that holds `$`, a backtick, a space or
another character the shell would change is not what the file shows, so
CashPilot asks you to retype it rather than run a shell to find out.

## How to import

1. Open **Settings**, then **Import** under **Import from another tool**.
2. Add `properties.conf`, plus `earnapp.txt` and `proxyrack.txt` if you have
   them. Leave the tool on "Tell from the files", or pick InternetIncome.
3. Press **Check what comes over**. Tick the services you want and fill in any
   value marked missing. Repocket needs your account email, which
   InternetIncome never asked for.
4. Press **Save the ticked services**, then **Deploy** each one from the same
   page, or later from the catalog: the deploy form opens with the saved values.

## What comes over

| InternetIncome app | Keys read | CashPilot service | What comes over |
|---|---|---|---|
| `repocket` | `REPOCKET_API` | Repocket | API key (add your email) |
| `traffmonetizer` | `TRAFFMONETIZER_TOKEN` | Traffmonetizer | token |
| `proxybase` | `PROXYBASE_ACCOUNT_ID` | ProxyBase | Access Token |
| `proxyrack` | `PROXYRACK_API`, `proxyrack.txt` | ProxyRack | API key, device UUID |
| `iproyals` | `IPROYALS_EMAIL`, `IPROYALS_PASSWORD` | IPRoyal Pawns | email, password |
| `honeygain` | `HONEYGAIN_EMAIL`, `HONEYGAIN_PASSWORD` | Honeygain | email, password |
| `packetstream` | `PACKETSTREAM_CID` | PacketStream | CID |
| `proxylite` | `PROXYLITE_USER_ID` | ProxyLite | user id |
| `earnfm` | `EARN_FM_API` | Earn.fm | API key |
| `earnapp` | `EARNAPP=true`, `earnapp.txt` | EarnApp | device id |
| `bitping` | `BITPING_EMAIL`, `BITPING_PASSWORD` | Bitping | email, password |
| `mysterium` | `MYSTERIUM=true` | Mysterium | turned on; see the Mysterium note below |

A blank value means the app was off, and it is not listed.

These apps are listed but not brought over:

| InternetIncome app | Why |
|---|---|
| `urnetwork` | InternetIncome uses a session token. CashPilot signs URnetwork in with your email and password. |
| `grass` | Grass runs as a browser extension. CashPilot only tracks it, with the access token from the Grass dashboard. |
| `titan`, `ebesucher` | CashPilot tracks these, but there is no container to deploy. |
| `wipter`, `peer2profit` | No longer available. |

Settings for apps CashPilot does not have (antGain, Adnade and others) are
listed by name as not read.

## What never comes over

- **`DEVICE_NAME`.** CashPilot names the device itself. Your old devices stay
  listed as offline in each provider's dashboard until you remove them there.
- **Proxies** (`USE_PROXIES`, `proxies.txt`). Add each one in the Proxies
  section of the Fleet page, then run copies of your services through it; adding
  a proxy starts nothing by itself. With `USE_PROXIES=true` InternetIncome ran
  your services only through proxies. Saving an import starts nothing either. A
  proxied copy needs the same service deployed on this computer first, and
  deploying it runs it on this computer's own connection, so a service that must
  never use that connection should stay unticked.
- **`BROWSER_USERNAME` and `BROWSER_PASSWORD`**, InternetIncome's own browser
  login.

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
the earnings tracking. For Honeygain, IPRoyal Pawns and Bitping the container
login is also the account login, so tracking works as soon as you save. Other
services need an earnings login the container never uses; add it in the deploy
form, or from **Settings > Earnings Collection**.

## Mysterium

Importing turns Mysterium on, but CashPilot starts a new node with a new
identity. The old node's keystore and any unsettled earnings stay in
InternetIncome's `mysterium-data` folder. Settle them first.

## Safety

- Nothing in the files is run or expanded, and no address in them is fetched.
- The files are read once and not kept. What you save is stored encrypted, like
  every other credential in CashPilot.
- Secrets are shown only as "set" on the page.
