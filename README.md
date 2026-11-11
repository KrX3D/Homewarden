# Homewarden

Homewarden keeps track of everything in your homelab: which devices are on, which
software versions they run, and what needs updating. Updates can then be run one
component at a time or all at once, manually, on a schedule, or triggered from
outside.

It runs as a single Docker container and is set up entirely through its web interface.
No playbooks or config files to write.

> **Status:** integrations for Linux (apt), Docker and OctoPrint are tested; Proxmox VE,
> Proxmox Backup Server, Unraid, UniFi, Windows, DeployDeck and Home Assistant are beta (version 0.9).
> Also included: an agent for hosts Homewarden cannot reach, schedules and update
> policies, API tokens, MQTT with Home Assistant discovery, notifications, backups
> with restore onto a replacement device.

## Features

- **Presence**: knows when devices that are only sometimes on (a 3D printer Pi, a backup
  server) come online and scans them once they are fully ready.
- **Version and update detection** per component, for example OctoPrint core, each
  plugin and the OctoPi OS separately.
- **Updates you control**: manual, scheduled, automatic, or triggered via API/MQTT,
  configured per device. Busy guards stop updates while a print job runs, and an
  optional backup can run first.
- **Rollback** to a previous image tag or the pre-update backup.
- **Backups** to an SMB share, with backup-age warnings.
- **Discovery** of containers, LXCs, VMs and network clients, plus a network scan over
  several ranges/VLANs at once, with an inbox to add or ignore what was found.
- **Monitoring**: uptime checks, resource history, disk health, certificate expiry.
- **Set up once per integration**: one credential and shared settings for all devices
  of a kind; a device only sets what differs.
- **Integrations** installable from a catalog (GitHub or your own Gitea): Linux/apt,
  Docker, OctoPrint first, then Unraid, Proxmox, Proxmox Backup Server, UniFi, Windows and
  Home Assistant (Core, OS, Supervisor, add-ons, HACS).
- Users with roles, optional 2FA, SSO via OIDC later. Light and dark mode.

## Running

### Unraid

Copy [`unraid/homewarden.xml`](unraid/homewarden.xml) to
`/boot/config/plugins/dockerMan/templates-user/my-homewarden.xml` on the flash drive,
then use Docker, Add Container, and pick "homewarden" from the template list.

### Docker

```sh
docker run -d --name homewarden \
  -p 8080:8080 \
  -v /mnt/user/appdata/homewarden:/data \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e PUID=99 -e PGID=100 -e TZ=Europe/Berlin \
  ghcr.io/krx3d/homewarden:latest
```

`PUID`/`PGID` set the user Homewarden runs as (default 99:100). The Docker socket is
optional and only needed to see and update containers on that host.

Homewarden is meant for the local network only. Put it behind your reverse proxy, but
do not expose it to the internet.

## Network and firewall

Homewarden connects **out** to your devices. Only the web port has to be reachable
**in**. When devices live in other VLANs, allow the Homewarden address to reach them on
the ports below. With Unraid's default bridge networking that address is the Unraid
server's IP; with `br0` or a custom network it is the container's own IP. The same
lists are shown in the app under Settings, Network, and in each device's Integration
card.

| From → to | Port | What for |
|---|---|---|
| Browser, agents, push scripts → Homewarden | TCP 8080 (or 443 via your reverse proxy) | web interface, agent connections, push monitors |
| Homewarden → devices | ICMP | online detection, network scan |
| Homewarden → devices | the TCP port you set as a device's online check | online detection |
| Homewarden → devices | TCP 22 | SSH: Linux (apt), Unraid, Proxmox updates, OctoPi packages, PBS packages |
| Homewarden → OctoPrint | TCP 80 (443 with HTTPS) | OctoPrint API |
| Homewarden → Proxmox VE / PBS | TCP 8006 / 8007 | Proxmox APIs |
| Homewarden → UniFi console | TCP 443 (8443 self-hosted) | UniFi API |
| Homewarden → DeployDeck | TCP 8080 | DeployDeck API |
| Homewarden → Home Assistant | TCP 8123 | Home Assistant API |
| Homewarden → remote Docker host | TCP 2375 | Docker socket proxy (the local socket needs no port) |
| Homewarden → NAS | TCP 445 | SMB backup target |
| Homewarden → MQTT broker | TCP 1883 | MQTT, Home Assistant discovery |
| Homewarden → internet | TCP 443 | image registries, integration catalog, notifications, SSO provider, release notes |
| Windows PC → share | TCP 445 | the PC runs your update scripts from the share |

The agent needs nothing open on the device: it connects to Homewarden.

## API

Everything the web interface does is available over HTTP. Create a token under
Settings, API tokens and send it as `Authorization: Bearer hw_...`. A token has its
user's rights; a **read-only** token can only send GET requests, which is enough to
review a setup. Settings, API tokens also has a switch that turns token access off
for everyone at once (the web interface, agents and push monitors keep working).

```sh
curl -H "Authorization: Bearer hw_..." http://homewarden:8080/api/devices
```

## License

[MIT](LICENSE)
