package run

import run "google.golang.org/api/run/v2"

func ServiceProperties(billsPerRequest bool, minInstances int, memory, ingress string) map[string]any {
	return PropertiesOf(&run.GoogleCloudRunV2Service{Ingress: ingress, Template: &run.GoogleCloudRunV2RevisionTemplate{
		Scaling: &run.GoogleCloudRunV2RevisionScaling{MinInstanceCount: int64(minInstances)},
		Containers: []*run.GoogleCloudRunV2Container{{Resources: &run.GoogleCloudRunV2ResourceRequirements{
			CpuIdle: billsPerRequest,
			Limits:  map[string]string{"cpu": RevisionCPU, "memory": memory},
		}}},
	}})
}

func PropertiesOf(service *run.GoogleCloudRunV2Service) map[string]any {
	template := service.Template
	scaling := map[string]any{"min_instance_count": 0}
	if template.Scaling != nil {
		scaling["min_instance_count"] = int(template.Scaling.MinInstanceCount)
		if template.Scaling.MaxInstanceCount > 0 {
			scaling["max_instance_count"] = int(template.Scaling.MaxInstanceCount)
		}
	}
	resources := template.Containers[0].Resources
	listed := map[string]any{
		"scaling": scaling,
		"containers": []any{map[string]any{
			"resources": map[string]any{
				"cpu_idle": resources.CpuIdle,
				"limits":   map[string]any{"cpu": resources.Limits["cpu"], "memory": resources.Limits["memory"]},
			},
		}},
	}
	if template.MaxInstanceRequestConcurrency > 0 {
		listed["max_instance_request_concurrency"] = int(template.MaxInstanceRequestConcurrency)
	}
	if template.Timeout != "" {
		listed["timeout"] = template.Timeout
	}
	return map[string]any{"ingress": service.Ingress, "template": listed}
}

const InvokerRole = "roles/run.invoker"
