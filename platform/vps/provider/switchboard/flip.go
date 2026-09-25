package switchboard

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
)

type Drain struct {
	Address string
	Held    int
	Expired bool
}

func (d Drain) String() string {
	if d.Expired {
		return fmt.Sprintf("%s %s %d", caddyadmin.DrainExpired, d.Address, d.Held)
	}
	return caddyadmin.Drained + " " + d.Address
}

type Upstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

func (b *Board) Flip(ctx context.Context, path string, retiring []string, window time.Duration, tell func(Drain)) error {
	keyed, err := addresses(retiring)
	if err != nil {
		return err
	}
	b.retire(keyed, 1)
	defer b.retire(keyed, -1)
	if err := b.Load(path); err != nil {
		return err
	}
	if len(keyed) == 0 {
		return nil
	}
	drained := make(chan string, len(keyed))
	stop := make(chan struct{})
	defer close(stop)
	pending := map[string]bool{}
	for _, address := range keyed {
		pending[address] = true
		quiet := b.ledger.quiet(address)
		go func() {
			select {
			case <-quiet:
				drained <- address
			case <-stop:
			}
		}()
	}
	ceiling := time.NewTimer(window)
	defer ceiling.Stop()
	for len(pending) > 0 {
		select {
		case address := <-drained:
			if pending[address] {
				delete(pending, address)
				tell(Drain{Address: address})
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-ceiling.C:
			for _, address := range slices.Sorted(maps.Keys(pending)) {
				tell(Drain{Address: address, Held: b.ledger.inFlight(address), Expired: true})
				if !b.table.Load().routed[address] {
					b.ledger.cut(address)
				}
			}
			return nil
		}
	}
	return nil
}

func (b *Board) retire(addresses []string, by int) {
	b.retiring.Lock()
	defer b.retiring.Unlock()
	for _, address := range addresses {
		b.draining[address] += by
		if b.draining[address] <= 0 {
			delete(b.draining, address)
		}
	}
}

func (b *Board) Idle(targets []string) ([]string, error) {
	keyed, err := addresses(targets)
	if err != nil {
		return nil, err
	}
	routed := b.table.Load().routed
	b.retiring.Lock()
	defer b.retiring.Unlock()
	var idle []string
	for at, address := range keyed {
		if !routed[address] && b.draining[address] == 0 {
			idle = append(idle, targets[at])
		}
	}
	return idle, nil
}

func (b *Board) Upstreams() []Upstream {
	counted := b.ledger.counts()
	for address := range b.table.Load().routed {
		if _, held := counted[address]; !held {
			counted[address] = 0
		}
	}
	listed := make([]Upstream, 0, len(counted))
	for _, address := range slices.Sorted(maps.Keys(counted)) {
		listed = append(listed, Upstream{Address: address, NumRequests: counted[address]})
	}
	return listed
}

func addresses(dials []string) ([]string, error) {
	keyed := make([]string, 0, len(dials))
	for _, dial := range dials {
		address, err := address(dial)
		if err != nil {
			return nil, err
		}
		keyed = append(keyed, address)
	}
	return keyed, nil
}
