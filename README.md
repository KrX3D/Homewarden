# Homewarden

Homewarden keeps track of everything in your homelab: which devices are on, which
software versions they run, and what needs updating. Updates can then be run one
component at a time or all at once, manually, on a schedule, or triggered from
outside.

It runs as a single Docker container and is set up entirely through its web interface.
No playbooks or config files to write.

> **Status: early development.** The image currently only serves a placeholder page.
> The features below are the plan, not what works today.

## Planned features

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

```sh
docker run -d --name homewarden \
  -p 8080:8080 \
  -v /mnt/user/appdata/homewarden:/data \
  ghcr.io/krx3d/homewarden:latest
```

Homewarden is meant for the local network only. Put it behind your reverse proxy, but
do not expose it to the internet.

## License

[MIT](LICENSE)
