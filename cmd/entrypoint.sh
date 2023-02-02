#!/bin/bash

# values to be replaced in Go
CANON_UID=__CANON_UID__
CANON_GID=__CANON_GID__
CANON_USER=__CANON_USER__
CANON_GROUP=__CANON_GROUP__

if [[ -e /var/run/docker.sock ]]; then
	groupmod --gid $(ls -n /var/run/docker.sock | cut -d" " -f4) docker 
fi

# group setup
getent group $CANON_GROUP > /dev/null 2&>1
if [[ $? -eq 0 ]]; then
	groupmod --non-unique --gid $CANON_GID $CANON_GROUP >/dev/null
else
	groupadd --non-unique --gid $CANON_GID $CANON_GROUP >/dev/null
fi

# user setup
getent passwd $CANON_USER > /dev/null 2&>1
if [[ $? -eq 0 ]]; then
	usermod --non-unique --uid $CANON_UID $CANON_USER >/dev/null
else
	useradd --non-unique --uid $CANON_UID --gid $CANON_GID $CANON_USER >/dev/null
fi

if [[ -e /run/host-services/ssh-auth.sock ]]; then
  chown $CANON_USER:$CANON_GROUP /run/host-services/ssh-auth.sock
fi

cat >> "$(getent passwd $CANON_USER | cut -d: -f6)/.bashrc" <<-EOS
ssh-add -l
ret=\$?
if [[ \$ret -ge 2 ]]; then
  eval \$(ssh-agent)
  ssh-add
elif [[ \$ret -eq 1 ]]; then
  ssh-add
fi

if ! grep -qs github.com ~/.netrc; then
  ssh git@github.com
  if [ \$? -eq 1 ]; then
    git config --global url.ssh://git@github.com/.insteadOf https://github.com/
  fi
fi
EOS

sudo --preserve-env=SSH_AUTH_SOCK -u $CANON_USER bash -lc "$@"