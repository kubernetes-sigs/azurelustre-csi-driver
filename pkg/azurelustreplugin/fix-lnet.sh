#!/bin/bash

# Issue #115 Remove workaround for LNET fix
# Delete this bash script

/usr/bin/logger "PID $$: Start fix-lnet"
count=1;

# try fix lnet 5 times maximum
for sleep_in_secs in 0 0.5 0.5 0.5 0.5; do
  sleep $sleep_in_secs

  break_flag=$(
    (
      break_flag_inner=false
      flock -w 60 -e ${FD}
      if sudo lnetctl net show --net tcp | grep "status: down"; then
        /usr/sbin/lnetctl net del --net tcp
        /usr/sbin/lnetctl net add --net tcp --if {default_interface}
        break_flag_inner=true
      fi
      echo $break_flag_inner
    ) {FD}< /etc/lustre/.lock
  )

  if [[ $break_flag == true ]]; then
    break
  else
    /usr/bin/logger "PID $$: Skipped fix-lnet, count=$count"
    count=$((count+1))
  fi
done
