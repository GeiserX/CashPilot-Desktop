# Supported services

> Some catalog links are affiliate/referral links -- see [Disclosure](#disclosure).

CashPilot bundles a catalog of 39 active passive-income services across multiple categories (the catalog also carries 11 retired entries, which the app hides and refuses to deploy). A representative selection is shown below.

## Before you start: how bandwidth-sharing works

Most services in this catalog are **bandwidth-sharing** apps (sometimes called proxyware). When you run one, it routes other people's internet traffic out through your home IP address and pays you a share of the fee. That traffic is usually ordinary web browsing -- but you don't choose it or see it, so it's worth understanding the trade before you opt in:

- **Your connection carries the traffic.** Requests from these networks look like they come from your IP, so only run this if you're comfortable with that.
- **Check your ISP's terms first.** Some ISP contracts prohibit reselling or sharing your connection, which can make bandwidth-sharing a terms-of-service problem even where it is otherwise legal. Read your own agreement before signing up.
- **It's all opt-in.** Start with the services you understand and add others only as you're comfortable.

Not every service resells your IP. **Compute and storage** providers -- Storj (spare disk space) and GPU services like Vast.ai and Salad (spare compute) -- pay for hardware resources, not for routing traffic through your connection. The catalog is a mix, so pick what suits you.

## Docker-Deployable Services

Services CashPilot can deploy and manage automatically via Docker containers.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout |
|---------|:-:|:-:|:-:|:-:|--------|
| [Anyone Protocol](https://anyone.io) | ✅ | ✅ | Unlimited | Undocumented | Crypto (ANYONE) |
| [Bitping](https://app.bitping.com) | ✅ | ✅ | Unlimited | Undocumented | Crypto (SOL) |
| [Earn.fm](https://earn.fm/ref/GEISYB91) | ✅ | ✅ | Unlimited | 1 | Crypto |
| [EarnApp](https://earnapp.com/i/TSMD9wSm) | ✅ | ❌ | 15 | Undocumented \* | PayPal, Gift Cards, Wise |
| [Honeygain](https://dashboard.honeygain.com/ref/SERGIB4014) | ✅ | ❌ | 10 | 1 | PayPal, Crypto |
| [IPRoyal Pawns](https://pawns.app?r=19266874) | ✅ | ❌ | Unlimited | 1 | PayPal, Crypto, Bank Transfer |
| [MystNodes](https://mystnodes.co/?referral_code=do7v7YOoBBpbOstKQovX2pUvZYKia4ZhH3QIdNtE) | ✅ | ✅ | Unlimited | Unlimited | Crypto (MYST) |
| [PacketStream](https://packetstream.io/?psr=7xgZ) | ✅ | ❌ | Unlimited | Undocumented | PayPal |
| [ProxyBase](https://peer.proxybase.org?referral=nXzS3c6iTO) | ✅ | ✅ | Unlimited | Undocumented | Crypto |
| [ProxyBase Markets](https://proxybase.xyz?referral=nXzS3c6iTO) | ✅ | ✅ | Unlimited | Undocumented | Crypto (USDC) |
| [ProxyLite](https://proxylite.ru/?r=KMUPRZIZ) | ✅ | ✅ | Unlimited | Undocumented | Crypto, PayPal |
| [ProxyRack](https://peer.proxyrack.com/ref/mpwiok3xlaxeycnn5znqlg7ipjeutxyxr6xl7vmn) | ✅ | ✅ | 500 | Undocumented | PayPal, Crypto |
| [Repocket](https://repocket.com/) | ✅ | ❌ | 5 | Undocumented | PayPal, Crypto |
| [Storj](https://storj.dev/node/get-started/setup) | ✅ | ✅ | Unlimited | Undocumented \*\* | Crypto (STORJ) |
| [Traffmonetizer](https://traffmonetizer.com/?aff=2111758) | ✅ | ✅ \*\*\* | Unlimited | Unlimited | Crypto (USDT), PayPal |
| [URnetwork](https://ur.io/?referral_code=1Q3G19) | ✅ | ✅ | Unlimited | Undocumented | Crypto |

> **Undocumented** means the provider publishes no per-IP device limit, not that
> there is none. Check the provider's terms before running a second instance behind
> one address. **Unlimited** is a limit the provider states it does not impose.
>
> \* EarnApp's own terms forbid running its software in containers, on virtual machines
> and on servers, which is exactly how CashPilot deploys it. The stated penalty is a
> terminated account with any pending payment cancelled. It is listed so the choice is
> yours, not hidden.
>
> \*\* Storj nodes on the same /24 subnet share data allocation, reducing per-node earnings.
>
> \*\*\* Traffmonetizer's Terms of Service require a residential IP; running it on a VPS may not comply with those terms, so check before you deploy.

## Browser Extension / Desktop Only

These services have no Docker image. CashPilot lists them in the catalog with signup links and earning estimates.

| Service | Residential IP | VPS IP | Devices / Acct | Devices / IP | Payout | Status |
|---------|:-:|:-:|:-:|:-:|--------|--------|
| [Bytebenefit](https://bytebenefit.io/invited?ref=Brl4z3) | ✅ | ❌ | Unlimited | Undocumented | PayPal | Active |
| [Bytelixir](https://bytelixir.com/r/OYEIRE0VSZBZ) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |
| [Dawn Internet](https://dawninternet.com/?code=2QLQV97F) | ✅ | ❌ | Unlimited | 1 | Crypto (DAWN) | Active |
| [Deeper Network](https://deeper.network) | ✅ | ❌ | Unlimited | 1 | Crypto (DPR) | Active |
| [Ebesucher](https://www.ebesucher.com/?ref=geiserx) | ✅ | ✅ | Unlimited | 1 | PayPal | Active |
| [Gradient Network](https://app.gradient.network/signup?referralCode=YSKMY7) | ✅ | ❌ | Unlimited | 1 | Crypto (GRADIENT) | Active |
| [Grass](https://app.grass.io/register?referralCode=kn8FNEPnUr2tMqE) | ✅ | ❌ | Unlimited | 1 | Crypto (GRASS) | Active |
| [Helium](https://helium.com) | ✅ | ❌ | Unlimited | 1 | Crypto (HNT) | Active |
| [Nodepay](https://app.nodepay.ai/register?ref=0wzzyznen64j9zx) | ✅ | ❌ | Unlimited | 1 | Crypto (NC) | Active |
| [Nodle](https://nodle.com) | ✅ | ✅ | Unlimited | 1 | Crypto (NODL) | Active |
| [PassiveApp](https://passiveapp.com/i/bqpC4M) | ✅ | ❌ | Unlimited | 1 | Crypto, PayPal | Active |
| [Sentinel dVPN](https://sentinel.co) | ✅ | ✅ | Unlimited | 1 | Crypto (DVPN) | Active |
| [Spide](https://spide.network/register.html?f3bc51) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |
| [Teneo Protocol](https://dashboard.teneo.pro/?code=CAqef) | ✅ | ❌ | Unlimited | 1 | Crypto (TENEO) | Active |
| [Theta Edge Node](https://thetatoken.org) | ✅ | ✅ | Unlimited | 1 | Crypto (TFUEL) | Active |
| [Titan Network](https://edge.titannet.info/signup?inviteCode=2GKKJ495) | ✅ | ❌ | Unlimited | 1 | Crypto (TNT) | Active |
| [Uprock](https://link.uprock.com/i/33e8492e) | ✅ | ❌ | Unlimited | 1 | Crypto | Active |

## GPU Compute

GPU-intensive computing services. Requires compatible hardware.

| Service | Residential IP | GPU Required | Min Storage | Payout | Status |
|---------|:-:|:-:|:-:|--------|--------|
| [Flux](https://runonflux.io) | ✅ | ❌ | 220GB | Crypto (FLUX) | Active |
| [Golem Network](https://golem.network) | ✅ | ❌ | 20GB | Crypto (GLM) | Active |
| [io.net](https://io.net) | ✅ | ✅ | N/A | Crypto (IO) | Active |
| [Nosana](https://nosana.io) | ✅ | ✅ | 50GB | Crypto (NOS) | Active |
| [Salad](https://salad.io) | ✅ | ✅ | N/A | PayPal, Gift Cards | Active |
| [Vast.ai](https://cloud.vast.ai/?ref_id=452772) | ✅ | ✅ | 100GB | Crypto, Bank Transfer | Active |

> **Note:** Earnings vary widely by location, hardware, and demand.

## Disclosure

> This project's service catalog may contain affiliate/referral links. If you sign up through them, the project maintainer may earn a small commission at no extra cost to you. This helps support development.
