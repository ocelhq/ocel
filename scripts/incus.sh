#!/usr/bin/bash
# Create
incus init images:ubuntu/24.04/cloud test-vps --vm \
    -c limits.cpu=2 \
    -c limits.memory=2GiB \
    -d root,size=20GiB

# creating ssh key (if not already done)
# ssh-keygen \
#     -t ed25519 \
#     -f ~/.ssh/incus-vps-test \
#     -N ''

# Configure SSH key - create key out of band
cat >/tmp/cloud-init.yaml <<EOF
#cloud-config
ssh_authorized_keys:
  - $(cat ~/.ssh/incus-vps-test.pub)
ssh_pwauth: false
packages:
  - openssh-server
EOF

incus config set test-vps cloud-init.user-data - < /tmp/cloud-init.yaml

# Boot
# incus start test-vps

# Inspect
# incus list

# accessing:
# ssh -i ~/.ssh/incus-vps-test ubuntu@<IP>

# on very first ssh, create snapshot so we can restore at any time after 'messing around' with testing
# incus snapshot create test-vps clean

# to get fresh starting point
# incus snapshot restore test-vps clean