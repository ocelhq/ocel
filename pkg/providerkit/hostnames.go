package providerkit

func AttributeHostnames(project []string, apps [][]string) [][]string {
	served := make([][]string, len(apps))
	seen := map[string]bool{}
	for slot, own := range apps {
		hosts := unseen(own, seen)
		if slot == 0 {
			hosts = append(hosts, unseen(project, seen)...)
		}
		served[slot] = hosts
	}
	return served
}

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
