#!/bin/bash

# values to be replaced in Go
CANON_UID=__CANON_UID__
CANON_GID=__CANON_GID__
CANON_USER=__CANON_USER__
CANON_GROUP=__CANON_GROUP__

echo "# Running canon setup tasks inside new container..."
if [[ -e /var/run/docker.sock ]] && getent group docker >/dev/null; then
	(set -x; groupmod --gid $(ls -n /var/run/docker.sock | cut -d" " -f4) docker)
fi

# check for conflicting group IDs
if getent group $CANON_GID >/dev/null; then
  CONFLICT_GROUP=$(getent group $CANON_GID | cut -d: -f1)
  if [[ $CONFLICT_GROUP != $CANON_GROUP ]]; then
    TEST_GID=$CANON_GID
    while [[ $TEST_GID -le 10000 ]]; do
      let TEST_GID++
      if ! getent group $TEST_GID >/dev/null; then
        break
      fi
    done
    echo "# Moving group with conflicting GID"
    (set -x; groupmod --gid $TEST_GID $CONFLICT_GROUP)
  fi
fi

# check for conflicting user IDs
if getent passwd $CANON_UID >/dev/null; then
  CONFLICT_USER=$(getent passwd $CANON_UID | cut -d: -f1)
  if [[ $CONFLICT_USER != $CANON_USER ]]; then
    TEST_UID=$CANON_UID
    while [[ $TEST_UID -le 10000 ]]; do
      let TEST_UID++
      if ! getent passwd $TEST_UID >/dev/null; then
        break
      fi
    done
    echo "# Moving user with conflicting UID"
    (set -x; usermod --uid $TEST_UID $CONFLICT_USER)
  fi
fi


if getent passwd $CANON_USER >/dev/null; then
  CANON_HOME=$(getent passwd $CANON_USER | cut -d: -f6)
  echo "# Fixing ownership on files in $CANON_HOME"
  echo "# This may take a while depending on the number of files."
  (set -x; chown -Rf $CANON_UID:$CANON_GID $CANON_HOME)
fi


# group setup
if getent group $CANON_GROUP >/dev/null; then
  echo "# Setting group GID to match profile"
	(set -x; groupmod --gid $CANON_GID $CANON_GROUP)
else
  echo "# Creating group per profile"
  (set -x; groupadd --gid $CANON_GID $CANON_GROUP)
fi

# user setup
if getent passwd $CANON_USER >/dev/null; then
  echo "# Setting user UID to match profile"
  (set -x; usermod --uid $CANON_UID $CANON_USER)
else
  echo "Creating user per profile"
	(set -x; useradd --uid $CANON_UID --gid $CANON_GID $CANON_USER)
fi

if ! grep -qs $CANON_USER /etc/sudoers; then
  echo "Adding $CANON_USER to /etc/sudoers"
  (set -x; echo "$CANON_USER ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers)
fi

if [[ -e /run/host-services/ssh-auth.sock ]]; then
  (set -x; chown $CANON_USER:$CANON_GROUP /run/host-services/ssh-auth.sock)
fi

if [[ -n "${CANON_SSH}" ]]; then
echo "# Writing SSH agent helpers to $(getent passwd $CANON_USER | cut -d: -f6)/.bashrc"
cat >> "$(getent passwd $CANON_USER | cut -d: -f6)/.bashrc" <<-EOS
# Canon SSH Setup
ssh-add -l >/dev/null
ret=\$?
if [[ \$ret -ge 2 ]]; then
  eval \$(ssh-agent)
  ssh-add
elif [[ \$ret -eq 1 ]]; then
  ssh-add
fi
# End Canon SSH Setup
EOS
fi

SHUTDOWN=0
trap 'SHUTDOWN=1' SIGTERM

# signals go that setup steps are complete and it's safe to call exec for the real commands
echo "CANON_READY"

until [[ $SHUTDOWN -gt 0 ]]; do
  sleep 1
done
