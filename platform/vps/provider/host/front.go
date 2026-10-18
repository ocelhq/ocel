package host

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func directoryPresent(f front.Proxy) string {
	at := resolvePlacedFile(f)
	if at == "" {
		return ""
	}
	dir := filepath.Dir(at)
	missing := fmt.Sprintf("option %q names the directory %s, which this box does not have: name the directory your proxy reads, or create it",
		f.Front().DirectoryOption(), dir)
	return "if [ ! -d " + shell.Quote(dir) + " ]; then\n" +
		"printf '%s\\n' " + shell.Quote(missing) + " >&2\n" +
		"exit 1\n" +
		"fi\n"
}

func (h *Host) refuseDirectoryMissing(ctx context.Context, fronting front.Proxy) error {
	present := directoryPresent(fronting)
	if present == "" {
		return nil
	}
	_, err := h.Run(ctx, ssh.Root, "find the directory "+fronting.Front().DirectoryOption()+" names", present, nil)
	return err
}

func switchboardNote(f front.Proxy) string {
	return "routes what your proxy forwards " + front.DescribeForwarding(f.Front())
}

func (h *Host) RouteBy(hostname string) string { return h.proxyOption.Front().RouteBy(hostname) }
