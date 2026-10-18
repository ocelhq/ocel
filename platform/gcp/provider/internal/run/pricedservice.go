package run

func PricedProperties(billsPerRequest bool, minInstances int, memory, ingress string) map[string]any {
	return map[string]any{
		"ingress": ingress,
		"template": map[string]any{
			"scaling": map[string]any{"min_instance_count": minInstances},
			"containers": []any{map[string]any{
				"resources": map[string]any{
					"cpu_idle": billsPerRequest,
					"limits":   map[string]any{"cpu": RevisionCPU, "memory": memory},
				},
			}},
		},
	}
}

const InvokerRole = "roles/run.invoker"
