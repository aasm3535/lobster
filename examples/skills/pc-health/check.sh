#!/usr/bin/env bash
echo "### Disk space"
df -h | sed -n '1p;/:/p'
echo
echo "### System"
uname -a
uptime 2>/dev/null || true
