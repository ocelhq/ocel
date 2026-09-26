package providerserver

func unseen(hosts []string, seen map[string]bool) []string {
	var out []string
	for _, host := range hosts {
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}
