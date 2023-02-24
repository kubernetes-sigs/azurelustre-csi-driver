#!/bin/bash

(
  flock -w 60 -e ${FD}
  if sudo lnetctl net show --net tcp | grep "status: down"; then
    /usr/sbin/lnetctl net del --net tcp
    /usr/sbin/lnetctl net add --net tcp --if {default_interface}
  fi
) {FD}< /etc/lustre/.lock
