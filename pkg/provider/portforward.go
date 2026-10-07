package provider

type PortForwardRequest struct {
	Bindings []Binding
}

type PortForward struct {
	Binding      string
	LocalAddress string
}
