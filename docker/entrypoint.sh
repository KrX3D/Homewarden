#!/bin/sh
# Starts Homewarden as PUID:PGID (default 99:100, Unraid's nobody:users).
#
# Nothing in Homewarden needs root. The container starts as root only long enough to
# create the user, fix ownership of the data folder, and give the user access to the
# Docker socket if one is mounted. Then it drops privileges for good.
# PUID=0 runs everything as root, for setups that really want that.
set -e

PUID="${PUID:-99}"
PGID="${PGID:-100}"
DATA="${HOMEWARDEN_DATA:-/data}"

if [ "$(id -u)" != "0" ]; then
    # Started with --user already; nothing to switch.
    exec "$@"
fi

mkdir -p "$DATA"

if [ "$PUID" = "0" ]; then
    exec "$@"
fi

group="$(getent group "$PGID" | cut -d: -f1)"
if [ -z "$group" ]; then
    addgroup -g "$PGID" homewarden
    group=homewarden
fi

user="$(getent passwd "$PUID" | cut -d: -f1)"
if [ -z "$user" ]; then
    adduser -D -H -u "$PUID" -G "$group" -s /sbin/nologin homewarden
    user=homewarden
fi

# The Docker socket belongs to whatever group the host uses. Add the user to a group
# with that id so it can talk to Docker without being root.
for sock in /var/run/docker.sock /run/docker.sock; do
    [ -S "$sock" ] || continue
    sgid="$(stat -c %g "$sock")"
    sgroup="$(getent group "$sgid" | cut -d: -f1)"
    if [ -z "$sgroup" ]; then
        addgroup -g "$sgid" dockersock
        sgroup=dockersock
    fi
    addgroup "$user" "$sgroup" 2>/dev/null || true
    break
done

# Only walk the whole tree when the top folder has the wrong owner, so a large data
# folder does not slow down every start.
if [ "$(stat -c %u:%g "$DATA")" != "$PUID:$PGID" ]; then
    chown -R "$PUID:$PGID" "$DATA"
fi

# su-exec with a user name (not uid:gid) also applies the supplementary groups, which
# is what makes the socket group above take effect.
exec su-exec "$user" "$@"
