package switchboard

import "time"

func (b *Board) DialConnectorAt(socket string) { b.connector = socket }

func (b *Board) RefreshTrustEvery(every time.Duration) { b.trust.every = every }
