# PROTOTYPE — throwaway, do not merge

This branch answers [#569](https://github.com/ocelhq/ocel/issues/569): drive one bootstrap
end to end against a real incus VM and find out what the spec has to fix.

`proto_ssh.go`, `proto_stack.go`, `proto_bootstrap.go` and `proto_records.go` replace the
conformant stub's `bootstrapper`/`credentials` with something that actually SSHes into a
machine and creates the core stack. It is throwaway: no tests, no conformance, no crypto
in the seal helper, no record CAS.

Run it against a VM:

    scripts/incus.sh create <name>
    go build -o /tmp/deploy ./platform/vps/provider/cmd/deploy
    # place /tmp/deploy at node_modules/@ocel/provider-vps-linux-x64/bin/deploy
    # in a project whose ocel.config.ts names the VM's address
    ocel bootstrap production

The findings live on the ticket.
