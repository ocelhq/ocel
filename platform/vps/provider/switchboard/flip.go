package switchboard

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"
)

const (
	DrainExpired = "drain-expired"
	Drained      = "drained"
	Ungated      = "ungated"
)

type Drain struct {
	Address  string
	InFlight int
	Expired  bool
}

func (d Drain) String() string {
	if d.Expired {
		return fmt.Sprintf("%s %s %d", DrainExpired, d.Address, d.InFlight)
	}
	return Drained + " " + d.Address
}

type Upstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

func (b *Board) Flip(ctx context.Context, path string, retiring []string, window time.Duration, tell func(Drain)) error {
	retirees, err := upstreamAddresses(retiring)
	if err != nil {
		return err
	}
	b.retire(retirees, 1)
	defer b.retire(retirees, -1)
	if err := b.Load(path); err != nil {
		return err
	}
	if len(retirees) == 0 {
		return nil
	}
	drained := make(chan string, len(retirees))
	stop := make(chan struct{})
	defer close(stop)
	pending := map[string]bool{}
	for _, address := range retirees {
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
				b.cutUnrouted(address)
				tell(Drain{Address: address})
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-ceiling.C:
			for _, address := range slices.Sorted(maps.Keys(pending)) {
				remaining := b.ledger.inFlight(address)
				b.cutUnrouted(address)
				tell(Drain{Address: address, InFlight: remaining, Expired: true})
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
	addresses, err := upstreamAddresses(targets)
	if err != nil {
		return nil, err
	}
	routed := b.table.Load().routed
	b.retiring.Lock()
	defer b.retiring.Unlock()
	var idle []string
	for at, address := range addresses {
		if !routed[address] && b.draining[address] == 0 {
			idle = append(idle, targets[at])
		}
	}
	return idle, nil
}

func (b *Board) Upstreams() []Upstream {
	counted := b.ledger.counts()
	for address := range b.table.Load().routed {
		if _, listed := counted[address]; !listed {
			counted[address] = 0
		}
	}
	listed := make([]Upstream, 0, len(counted))
	for _, address := range slices.Sorted(maps.Keys(counted)) {
		listed = append(listed, Upstream{Address: address, NumRequests: counted[address]})
	}
	return listed
}

func upstreamAddresses(dials []string) ([]string, error) {
	addresses := make([]string, 0, len(dials))
	for _, dial := range dials {
		address, err := UpstreamAddress(dial)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}
