# Homewarden

Homewarden keeps track of everything in your homelab: which devices are on, which
software versions they run, and what needs updating. Updates can then be run one
component at a time or all at once, manually, on a schedule, or triggered from
outside.

It runs as a single Docker container and is set up entirely through its web interface.
No playbooks or config files to write.

> **Status:** integrations for Linux (apt), Docker and OctoPrint are tested; Proxmox VE,
> Proxmox Backup Server, Unraid, UniFi, Windows and DeployDeck are beta (version 0.9).
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
- **Discovery** of containers, LXCs, VMs and network clients, with an inbox to add or
  ignore what was found.
- **Monitoring**: uptime checks, resource history, disk health, certificate expiry.
- **Integrations** installable from a catalog (GitHub or your own Gitea): Linux/apt,
  Docker, OctoPrint first, then Unraid, Proxmox, Proxmox Backup Server, UniFi and Windows.
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

## License

[MIT](LICENSE)
